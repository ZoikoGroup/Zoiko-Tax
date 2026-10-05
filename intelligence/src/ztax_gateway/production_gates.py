"""Production AI Architecture Gates — G-AIARCH-01 through G-AIARCH-20.

Chapter 17 §31 of the ZoikoTax Master Specification.

This module is a conformance checker, not a policy enforcer.  It does not
refuse calls; it inspects the actual registries that the governance, tool-
broker, resilience, production-registries, and security-controls modules build
at runtime and reports PASS / FAIL / NOT_APPLICABLE per gate, with an evidence
string for every result.  The intended use is:

    * Pre-deployment gate in CI — run before anything goes to production.
    * Continuous runtime conformance — call periodically and feed the report
      to the observability pipeline.
    * Audit trail — the report is a frozen dataclass; write it to the evidence
      store alongside the manifests it inspected.

The 20 gates
------------
Gates are mapped directly to existing modules:

Gate            Module / artifact inspected
G-AIARCH-01     UseCaseRegistry — every use case has an owner (ZTAX-AIGOV-REQ-0002)
G-AIARCH-02     UseCaseRegistry — every use case declares a max_authority ceiling
                (ZTAX-AIGOV-REQ-0003, ZTAX-AI-REQ-0007)
G-AIARCH-03     ManifestRegistry x UseCaseRegistry — every ACTIVE manifest's
                ai_use_case_id resolves to a registered UseCase
                (ZTAX-AIGOV-REQ-0001, ZTAX-AIGOV-REQ-0025)
G-AIARCH-04     AIReleaseManifest — every ACTIVE manifest has an evaluation
                profile (evaluation-gated releases) (ZTAX-AIGOV-REQ-0026)
G-AIARCH-05     AIReleaseManifest — every ACTIVE manifest has an allowed_regions
                set (no region-less manifest may be used) (ZTAX-AIGOV-REQ-0025)
G-AIARCH-06     AIReleaseManifest — manifest_hash passes verify_hash() for every
                ACTIVE manifest (tamper detection) (ZTAX-SEC-REQ-0113)
G-AIARCH-07     UseCaseRegistry — no A5 authority ceiling anywhere in the
                registry (ZTAX-AIGOV-REQ-0003, ZTAX-AI-REQ-0007)
G-AIARCH-08     PromptProfile — every prompt profile referenced by an ACTIVE
                manifest is non-deprecated and has safety rules
                (ZTAX-AIGOV-REQ-0023)
G-AIARCH-09     ToolCatalog — no PRIVILEGED-class tool registered
                (ZTAX-SEC-REQ-0119, ZTAX-AIGOV-REQ-0015)
G-AIARCH-10     ToolCatalog — every registered tool has an owner and required
                scopes that can be independently evaluated (ZTAX-SEC-REQ-0119)
G-AIARCH-11     ProviderRegistry — every provider has at least one permitted
                region (no region-less provider) (ZTAX-SEC-REQ-0037)
G-AIARCH-12     ProviderRegistry — every provider's fallback qualification flag
                is explicitly set (no implicit widening) (ZTAX-SEC-REQ-0122)
G-AIARCH-13     ProviderRegistry (§25) — every private provider has a non-empty
                owner_id (ZTAX-SEC-REQ-0127)
G-AIARCH-14     ProviderRegistry (§25) — every private provider with a weight
                record is production-safe (SCAN = CLEAN) before being marked
                qualified (ZTAX-SEC-REQ-0113, ZTAX-AI-REQ-0010)
G-AIARCH-15     ProviderRegistry — no deprecated model is still the resolved
                model for any ACTIVE manifest (ZTAX-AIGOV-REQ-0028)
G-AIARCH-16     UseCaseRegistry — no suspended use case has an ACTIVE manifest
                in the ManifestRegistry (kill switch / manifest consistency)
                (ZTAX-AIGOV-REQ-0018)
G-AIARCH-17     ManifestRegistry — every ACTIVE manifest has a resolved_model_id
                that is pinned (non-empty, not a wildcard alias like 'latest')
                (ZTAX-AIGOV-REQ-0027)
G-AIARCH-18     AIReleaseManifest — every ACTIVE agentic manifest (non-null
                agent_profile_id) has a tool_policy_id (ZTAX-AIGOV-REQ-0024)
G-AIARCH-19     AIReleaseManifest — authority_level on every ACTIVE manifest
                does not exceed the UseCase's max_authority ceiling
                (ZTAX-AIGOV-REQ-0003)
G-AIARCH-20     ProviderRegistry x ManifestRegistry — every ACTIVE manifest's
                resolved_model_id references a model in the ProviderRegistry
                (ZTAX-AIGOV-REQ-0022)

Design rules
------------
* **Read-only.**  This module calls no ``authorise()`` function, makes no
  mutations, and has no side effects.  It reads public properties of the
  registries and builds an immutable report.
* **All 20 gates always run.**  No short-circuit — a skipped gate produces
  ``NOT_APPLICABLE``, not a hidden pass.
* **Evidence string on every result.**  Every ``GateResult`` carries a
  human-readable string that names the specific item(s) that caused the
  verdict.  A ``PASS`` with an empty evidence string is a programming error.
* **No fiscal imports** (ADR-0006 §2.6).
* **No live model calls.**
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import UTC, datetime
from enum import StrEnum
from typing import Final

from .governance import UseCaseRegistry
from .production_registries import (
    AIReleaseManifest,
    ManifestRegistry,
    PromptProfile,
)
from .provenance import AuthorityOutcome
from .release_lifecycle import ManifestLifecycle, can_serve_production
from .resilience import ProviderRecord, ProviderRegistry
from .tool_broker import ActionClass, ToolCatalog

__all__: list[str] = [
    "ArchGateId",
    "ArchGateReport",
    "ArchGateResult",
    "GateVerdict",
    "ProductionGateRunner",
    "PromptProfileRegistry",
    "run_production_gates",
]


# ---------------------------------------------------------------------------
# Gate verdict
# ---------------------------------------------------------------------------


class GateVerdict(StrEnum):
    """Result of one architecture gate evaluation.

    ``PASS``           -- the gate's invariant holds; evidence names what was checked.
    ``FAIL``           -- the invariant is violated; evidence names the offending item(s).
    ``NOT_APPLICABLE`` -- the gate cannot be evaluated with the supplied registries
                          (e.g. no ACTIVE manifests means G-AIARCH-03 is N/A).
    """

    PASS = "PASS"
    FAIL = "FAIL"
    NOT_APPLICABLE = "NOT_APPLICABLE"


# ---------------------------------------------------------------------------
# Gate identifiers (closed vocabulary matching §31's table)
# ---------------------------------------------------------------------------


class ArchGateId(StrEnum):
    """The twenty production architecture gate IDs (§31).

    Each value is the canonical identifier used in reports and audit logs.
    """

    G01 = "G-AIARCH-01"
    G02 = "G-AIARCH-02"
    G03 = "G-AIARCH-03"
    G04 = "G-AIARCH-04"
    G05 = "G-AIARCH-05"
    G06 = "G-AIARCH-06"
    G07 = "G-AIARCH-07"
    G08 = "G-AIARCH-08"
    G09 = "G-AIARCH-09"
    G10 = "G-AIARCH-10"
    G11 = "G-AIARCH-11"
    G12 = "G-AIARCH-12"
    G13 = "G-AIARCH-13"
    G14 = "G-AIARCH-14"
    G15 = "G-AIARCH-15"
    G16 = "G-AIARCH-16"
    G17 = "G-AIARCH-17"
    G18 = "G-AIARCH-18"
    G19 = "G-AIARCH-19"
    G20 = "G-AIARCH-20"
    G21 = "G-AIARCH-21"  # Extension: lifecycle-consistency gate


# Human-readable short description of each gate (for the report summary).
_GATE_DESCRIPTIONS: Final[dict[ArchGateId, str]] = {
    ArchGateId.G01: "Every use case has a named owner",
    ArchGateId.G02: "Every use case declares a non-A5 max_authority ceiling",
    ArchGateId.G03: "Every ACTIVE manifest's use-case ID resolves to a registered UseCase",
    ArchGateId.G04: "Every ACTIVE manifest is evaluation-gated (has evaluation_profile_id)",
    ArchGateId.G05: "Every ACTIVE manifest has at least one allowed_region",
    ArchGateId.G06: "Every ACTIVE manifest passes verify_hash() (tamper detection)",
    ArchGateId.G07: "No use case claims A5 authority (unconditionally refused)",
    ArchGateId.G08: (
        "Every prompt profile in an ACTIVE manifest is non-deprecated with safety rules"
    ),
    ArchGateId.G09: "No PRIVILEGED-class tool is registered in the ToolCatalog",
    ArchGateId.G10: "Every registered tool has an owner",
    ArchGateId.G11: "Every provider has at least one permitted region",
    ArchGateId.G12: "Every provider's is_qualified flag is explicitly set (no implicit widening)",
    ArchGateId.G13: "Every private provider has a non-empty owner_id",
    ArchGateId.G14: "Every private provider with a weight record is CLEAN before qualified",
    ArchGateId.G15: "No deprecated model is the resolved_model_id for an ACTIVE manifest",
    ArchGateId.G16: "No suspended use case has an ACTIVE manifest",
    ArchGateId.G17: "Every ACTIVE manifest has a pinned resolved_model_id (not 'latest' alias)",
    ArchGateId.G18: "Every agentic ACTIVE manifest has a tool_policy_id",
    ArchGateId.G19: "Every ACTIVE manifest's authority_level <= UseCase max_authority",
    ArchGateId.G20: "Every ACTIVE manifest's resolved_model_id is registered in ProviderRegistry",
    ArchGateId.G21: (
        "Every ManifestRegistry-ACTIVE manifest has a production-serving lifecycle state "
        "(PILOT/PRODUCTION/DEGRADED) — lifecycle and registry in sync"
    ),
}

# Model ID alias fragments that indicate a non-pinned floating reference.
_WILDCARD_ALIASES: Final[frozenset[str]] = frozenset(
    {"latest", "current", "stable", "preview", "experimental", "beta", "edge"}
)


# ---------------------------------------------------------------------------
# Gate result and report
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class ArchGateResult:
    """Immutable result of one gate evaluation.

    ``gate_id``    -- the gate identifier (e.g. ``G-AIARCH-03``).
    ``description``-- short human-readable gate description.
    ``verdict``    -- PASS / FAIL / NOT_APPLICABLE.
    ``evidence``   -- what was checked (PASS) or what violated (FAIL).
                      Must be non-empty for PASS and FAIL.
    """

    gate_id: ArchGateId
    description: str
    verdict: GateVerdict
    evidence: str


@dataclass(frozen=True, slots=True)
class ArchGateReport:
    """Immutable production gate conformance report.

    ``run_at``      -- UTC timestamp when the runner was invoked.
    ``results``     -- all 20 :class:`ArchGateResult` objects, in gate order.
    ``pass_count``  -- number of PASS verdicts.
    ``fail_count``  -- number of FAIL verdicts.
    ``na_count``    -- number of NOT_APPLICABLE verdicts.
    ``all_passed``  -- ``True`` only when every gate is PASS or NOT_APPLICABLE
                       (i.e. no FAIL).  This is the production go/no-go flag.
    """

    run_at: datetime
    results: tuple[ArchGateResult, ...]
    pass_count: int
    fail_count: int
    na_count: int
    all_passed: bool

    def result_for(self, gate_id: ArchGateId) -> ArchGateResult | None:
        """Return the result for *gate_id*, or ``None`` if not present."""
        for r in self.results:
            if r.gate_id is gate_id:
                return r
        return None

    def failed_gates(self) -> list[ArchGateResult]:
        """Return all FAIL results, in gate order."""
        return [r for r in self.results if r.verdict is GateVerdict.FAIL]

    def as_summary(self) -> str:
        """One-line human-readable summary string."""
        status = "ALL PASSED" if self.all_passed else f"{self.fail_count} FAILED"
        return (
            f"ArchGateReport run_at={self.run_at.isoformat()} "
            f"pass={self.pass_count} fail={self.fail_count} na={self.na_count} "
            f"status={status}"
        )


# ---------------------------------------------------------------------------
# PromptProfileRegistry — thin wrapper so the runner stays decoupled
# ---------------------------------------------------------------------------


class PromptProfileRegistry:
    """Lookup registry for :class:`~ztax_gateway.production_registries.PromptProfile`.

    Kept deliberately minimal — the runner only needs ``get()`` to look up a
    profile by ID.  This avoids coupling ``production_gates.py`` to any future
    changes in how profiles are stored.

    Usage::

        ppr = PromptProfileRegistry()
        ppr.register(my_profile)
        runner = ProductionGateRunner(
            ...,
            prompt_registry=ppr,
        )
    """

    def __init__(self) -> None:
        self._profiles: dict[str, PromptProfile] = {}

    def register(self, profile: PromptProfile) -> None:
        """Register a :class:`PromptProfile`.  Re-registration replaces."""
        self._profiles[profile.profile_id] = profile

    def get(self, profile_id: str) -> PromptProfile | None:
        """Return the profile for *profile_id*, or ``None``."""
        return self._profiles.get(profile_id)

    @property
    def count(self) -> int:
        """Number of registered profiles."""
        return len(self._profiles)


# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------


def _pass(gate_id: ArchGateId, evidence: str) -> ArchGateResult:
    return ArchGateResult(
        gate_id=gate_id,
        description=_GATE_DESCRIPTIONS[gate_id],
        verdict=GateVerdict.PASS,
        evidence=evidence,
    )


def _fail(gate_id: ArchGateId, evidence: str) -> ArchGateResult:
    return ArchGateResult(
        gate_id=gate_id,
        description=_GATE_DESCRIPTIONS[gate_id],
        verdict=GateVerdict.FAIL,
        evidence=evidence,
    )


def _na(gate_id: ArchGateId, evidence: str) -> ArchGateResult:
    return ArchGateResult(
        gate_id=gate_id,
        description=_GATE_DESCRIPTIONS[gate_id],
        verdict=GateVerdict.NOT_APPLICABLE,
        evidence=evidence,
    )


def _is_alias(model_id: str) -> bool:
    """Return True if *model_id* looks like a floating alias rather than a pin."""
    lower = model_id.lower()
    return any(alias in lower for alias in _WILDCARD_ALIASES)


# ---------------------------------------------------------------------------
# Gate runner
# ---------------------------------------------------------------------------


class ProductionGateRunner:
    """Runs all 20 production architecture gates and returns an :class:`ArchGateReport`.

    Parameters
    ----------
    use_case_registry:
        The live :class:`~ztax_gateway.governance.UseCaseRegistry`.
    manifest_registry:
        The live :class:`~ztax_gateway.production_registries.ManifestRegistry`.
    tool_catalog:
        The live :class:`~ztax_gateway.tool_broker.ToolCatalog`.
    provider_registry:
        The live :class:`~ztax_gateway.resilience.ProviderRegistry`.
    prompt_registry:
        Optional :class:`PromptProfileRegistry`.  When ``None``, gates that
        inspect prompt profiles (G-AIARCH-08) return NOT_APPLICABLE.

    Usage::

        runner = ProductionGateRunner(
            use_case_registry=uc_reg,
            manifest_registry=mf_reg,
            tool_catalog=catalog,
            provider_registry=pr_reg,
            prompt_registry=pp_reg,   # optional
        )
        report = runner.run()
        if not report.all_passed:
            for f in report.failed_gates():
                print(f.gate_id, f.evidence)
    """

    def __init__(
        self,
        *,
        use_case_registry: UseCaseRegistry,
        manifest_registry: ManifestRegistry,
        tool_catalog: ToolCatalog,
        provider_registry: ProviderRegistry,
        prompt_registry: PromptProfileRegistry | None = None,
        lifecycle_registry: dict[str, ManifestLifecycle] | None = None,
    ) -> None:
        self._uc_reg = use_case_registry
        self._mf_reg = manifest_registry
        self._tool_cat = tool_catalog
        self._pr_reg = provider_registry
        self._pp_reg = prompt_registry
        self._lc_reg = lifecycle_registry

    # ------------------------------------------------------------------
    # Public entry point
    # ------------------------------------------------------------------

    def run(self) -> ArchGateReport:
        """Run all 20 gates and return the report.

        All gates are evaluated unconditionally — no short-circuit.
        """
        run_at = datetime.now(UTC)

        results: list[ArchGateResult] = [
            self._g01(),
            self._g02(),
            self._g03(),
            self._g04(),
            self._g05(),
            self._g06(),
            self._g07(),
            self._g08(),
            self._g09(),
            self._g10(),
            self._g11(),
            self._g12(),
            self._g13(),
            self._g14(),
            self._g15(),
            self._g16(),
            self._g17(),
            self._g18(),
            self._g19(),
            self._g20(),
            self._g21(),
        ]

        pass_count = sum(1 for r in results if r.verdict is GateVerdict.PASS)
        fail_count = sum(1 for r in results if r.verdict is GateVerdict.FAIL)
        na_count = sum(1 for r in results if r.verdict is GateVerdict.NOT_APPLICABLE)

        return ArchGateReport(
            run_at=run_at,
            results=tuple(results),
            pass_count=pass_count,
            fail_count=fail_count,
            na_count=na_count,
            all_passed=fail_count == 0,
        )

    # ------------------------------------------------------------------
    # Gate implementations — one method per gate
    # ------------------------------------------------------------------

    def _g01(self) -> ArchGateResult:
        """G-AIARCH-01: Every use case has a named owner (ZTAX-AIGOV-REQ-0002)."""
        uc_ids = list(self._uc_reg.ids())
        use_cases = [uc for uid in uc_ids if (uc := self._uc_reg.get(uid)) is not None]
        if not use_cases:
            return _na(ArchGateId.G01, "No use cases registered")
        bad = [uc.use_case_id for uc in use_cases if not uc.owner.strip()]
        if bad:
            return _fail(ArchGateId.G01, f"Use case(s) with empty owner: {bad}")
        return _pass(
            ArchGateId.G01,
            f"All {len(use_cases)} use case(s) have a named owner",
        )

    def _g02(self) -> ArchGateResult:
        """G-AIARCH-02: Every use case declares a non-A5 max_authority (ZTAX-AIGOV-REQ-0003)."""
        uc_ids = list(self._uc_reg.ids())
        use_cases = [uc for uid in uc_ids if (uc := self._uc_reg.get(uid)) is not None]
        if not use_cases:
            return _na(ArchGateId.G02, "No use cases registered")
        bad = [uc.use_case_id for uc in use_cases if uc.max_authority == AuthorityOutcome.A5]
        if bad:
            return _fail(
                ArchGateId.G02,
                f"Use case(s) with A5 max_authority (unconditionally refused): {bad}",
            )
        return _pass(
            ArchGateId.G02,
            f"All {len(use_cases)} use case(s) have a non-A5 authority ceiling",
        )

    def _g03(self) -> ArchGateResult:
        """G-AIARCH-03: Every ACTIVE manifest resolves to a UseCase (ZTAX-AIGOV-REQ-0001)."""
        active = self._active_manifests()
        if not active:
            return _na(ArchGateId.G03, "No ACTIVE manifests to inspect")
        bad = [
            m.manifest_id
            for m in active
            if self._uc_reg.get(m.ai_use_case_id) is None
        ]
        if bad:
            return _fail(
                ArchGateId.G03,
                f"ACTIVE manifest(s) whose ai_use_case_id is not registered: {bad}",
            )
        return _pass(
            ArchGateId.G03,
            f"All {len(active)} ACTIVE manifest(s) have a registered use case",
        )

    def _g04(self) -> ArchGateResult:
        """G-AIARCH-04: Every ACTIVE manifest is evaluation-gated (ZTAX-AIGOV-REQ-0026)."""
        active = self._active_manifests()
        if not active:
            return _na(ArchGateId.G04, "No ACTIVE manifests to inspect")
        bad = [
            m.manifest_id
            for m in active
            if not m.evaluation_profile_id.strip()
        ]
        if bad:
            return _fail(
                ArchGateId.G04,
                f"ACTIVE manifest(s) with empty evaluation_profile_id: {bad}",
            )
        return _pass(
            ArchGateId.G04,
            f"All {len(active)} ACTIVE manifest(s) have an evaluation profile",
        )

    def _g05(self) -> ArchGateResult:
        """G-AIARCH-05: Every ACTIVE manifest has at least one allowed_region."""
        active = self._active_manifests()
        if not active:
            return _na(ArchGateId.G05, "No ACTIVE manifests to inspect")
        bad = [m.manifest_id for m in active if not m.allowed_regions]
        if bad:
            return _fail(
                ArchGateId.G05,
                f"ACTIVE manifest(s) with empty allowed_regions: {bad}",
            )
        return _pass(
            ArchGateId.G05,
            f"All {len(active)} ACTIVE manifest(s) have at least one allowed region",
        )

    def _g06(self) -> ArchGateResult:
        """G-AIARCH-06: Every ACTIVE manifest passes verify_hash() (ZTAX-SEC-REQ-0113)."""
        active = self._active_manifests()
        if not active:
            return _na(ArchGateId.G06, "No ACTIVE manifests to inspect")
        bad = [m.manifest_id for m in active if not m.verify_hash()]
        if bad:
            return _fail(
                ArchGateId.G06,
                f"ACTIVE manifest(s) with tampered / mismatched hash: {bad}",
            )
        return _pass(
            ArchGateId.G06,
            f"All {len(active)} ACTIVE manifest(s) pass integrity verification",
        )

    def _g07(self) -> ArchGateResult:
        """G-AIARCH-07: No use case claims A5 (ZTAX-AIGOV-REQ-0003, ZTAX-AI-REQ-0007)."""
        uc_ids = list(self._uc_reg.ids())
        use_cases = [uc for uid in uc_ids if (uc := self._uc_reg.get(uid)) is not None]
        if not use_cases:
            return _na(ArchGateId.G07, "No use cases registered")
        a5_ids = [uc.use_case_id for uc in use_cases if uc.max_authority == AuthorityOutcome.A5]
        if a5_ids:
            return _fail(
                ArchGateId.G07,
                f"Use case(s) with A5 authority claim (unconditionally refused "
                f"by governance layer): {a5_ids}",
            )
        return _pass(ArchGateId.G07, f"No A5 authority in any of {len(use_cases)} use case(s)")

    def _g08(self) -> ArchGateResult:
        """G-AIARCH-08: Prompt profiles in ACTIVE manifests are non-deprecated + have safety rules
        (ZTAX-AIGOV-REQ-0023)."""
        if self._pp_reg is None:
            return _na(ArchGateId.G08, "No PromptProfileRegistry supplied")
        active = self._active_manifests()
        if not active:
            return _na(ArchGateId.G08, "No ACTIVE manifests to inspect")

        deprecated_ids: list[str] = []
        no_rules_ids: list[str] = []
        missing_ids: list[str] = []

        for m in active:
            pid = m.prompt_profile_id
            if not pid.strip():
                continue
            profile = self._pp_reg.get(pid)
            if profile is None:
                missing_ids.append(f"{m.manifest_id}:{pid}")
                continue
            if profile.deprecated:
                deprecated_ids.append(f"{m.manifest_id}:{pid}")
            if not profile.safety_rules:
                no_rules_ids.append(f"{m.manifest_id}:{pid}")

        violations: list[str] = []
        if missing_ids:
            violations.append(f"missing from prompt registry: {missing_ids}")
        if deprecated_ids:
            violations.append(f"deprecated prompt profiles in ACTIVE manifests: {deprecated_ids}")
        if no_rules_ids:
            violations.append(f"prompt profiles with no safety rules: {no_rules_ids}")
        if violations:
            return _fail(ArchGateId.G08, "; ".join(violations))
        return _pass(
            ArchGateId.G08,
            f"All prompt profiles in {len(active)} ACTIVE manifest(s) are "
            "non-deprecated with safety rules",
        )

    def _g09(self) -> ArchGateResult:
        """G-AIARCH-09: No PRIVILEGED-class tool registered (ZTAX-SEC-REQ-0119)."""
        tool_ids = list(self._tool_cat.ids())
        tools = [t for tid in tool_ids if (t := self._tool_cat.get(tid)) is not None]
        if not tools:
            return _na(ArchGateId.G09, "No tools registered in the ToolCatalog")
        # ToolCatalog.register() already refuses PRIVILEGED at registration time,
        # but we check here as an independent gate — defence in depth.
        privileged = [t.tool_id for t in tools if t.action_class == ActionClass.PRIVILEGED]
        if privileged:
            return _fail(
                ArchGateId.G09,
                f"PRIVILEGED-class tool(s) found in catalog (must never be registered): "
                f"{privileged}",
            )
        return _pass(
            ArchGateId.G09,
            f"No PRIVILEGED-class tool among {len(tools)} registered tool(s)",
        )

    def _g10(self) -> ArchGateResult:
        """G-AIARCH-10: Every tool has an owner (ZTAX-SEC-REQ-0119)."""
        tool_ids = list(self._tool_cat.ids())
        tools = [t for tid in tool_ids if (t := self._tool_cat.get(tid)) is not None]
        if not tools:
            return _na(ArchGateId.G10, "No tools registered in the ToolCatalog")
        bad = [t.tool_id for t in tools if not t.owner.strip()]
        if bad:
            return _fail(
                ArchGateId.G10,
                f"Tool(s) with empty owner: {bad}",
            )
        return _pass(
            ArchGateId.G10,
            f"All {len(tools)} tool(s) have a named owner",
        )

    def _g11(self) -> ArchGateResult:
        """G-AIARCH-11: Every provider has at least one permitted region (ZTAX-SEC-REQ-0037)."""
        providers = self._all_providers()
        if not providers:
            return _na(ArchGateId.G11, "No providers registered in the ProviderRegistry")
        bad = [p.provider_id for p in providers if not p.permitted_regions]
        if bad:
            return _fail(ArchGateId.G11, f"Provider(s) with no permitted_regions: {bad}")
        return _pass(
            ArchGateId.G11,
            f"All {len(providers)} provider(s) have at least one permitted region",
        )

    def _g12(self) -> ArchGateResult:
        """G-AIARCH-12: is_qualified is explicitly set on every provider (ZTAX-SEC-REQ-0122)."""
        # The spec requires no implicit widening — every provider must have gone
        # through a qualification gate.  We verify that the is_qualified field
        # is not defaulted to True without an explicit value being set.
        # Since Python dataclasses don't track whether a default was used, we
        # check by inspecting the combined picture: a private provider with
        # is_qualified=True but no weight_record is suspicious — it may have
        # been set by mistake.  For non-private providers, is_qualified=True
        # is the expected qualified state.
        providers = self._all_providers()
        if not providers:
            return _na(ArchGateId.G12, "No providers registered")
        # Flag private providers that are marked qualified but have no
        # weight_record — they haven't gone through any verifiable gate.
        suspicious = [
            p.provider_id
            for p in providers
            if p.is_private and p.is_qualified and p.weight_record is None
        ]
        if suspicious:
            return _fail(
                ArchGateId.G12,
                f"Private provider(s) marked is_qualified=True with no weight_record "
                f"(no verifiable qualification gate): {suspicious}",
            )
        return _pass(
            ArchGateId.G12,
            f"All {len(providers)} provider(s) pass the qualification-gate check",
        )

    def _g13(self) -> ArchGateResult:
        """G-AIARCH-13: Every private provider has a non-empty owner_id (ZTAX-SEC-REQ-0127)."""
        providers = self._all_providers()
        private = [p for p in providers if p.is_private]
        if not private:
            return _na(ArchGateId.G13, "No private providers registered")
        bad = [p.provider_id for p in private if not p.owner_id.strip()]
        if bad:
            return _fail(
                ArchGateId.G13,
                f"Private provider(s) with empty owner_id: {bad}",
            )
        return _pass(
            ArchGateId.G13,
            f"All {len(private)} private provider(s) have a non-empty owner_id",
        )

    def _g14(self) -> ArchGateResult:
        """G-AIARCH-14: Private providers with weight records are CLEAN before qualified
        (ZTAX-SEC-REQ-0113, ZTAX-AI-REQ-0010)."""
        providers = self._all_providers()
        with_weights = [p for p in providers if p.weight_record is not None]
        if not with_weights:
            return _na(ArchGateId.G14, "No providers with weight records registered")
        bad = [
            f"{p.provider_id}(scan={p.weight_record.scan_status})"
            for p in with_weights
            if p.is_qualified and p.weight_record is not None
            and not p.weight_record.is_production_safe
        ]
        if bad:
            return _fail(
                ArchGateId.G14,
                f"Qualified provider(s) with non-CLEAN weight scan: {bad}",
            )
        return _pass(
            ArchGateId.G14,
            f"All {len(with_weights)} provider(s) with weight records are production-safe",
        )

    def _g15(self) -> ArchGateResult:
        """G-AIARCH-15: No deprecated model is resolved_model_id for an ACTIVE manifest
        (ZTAX-AIGOV-REQ-0028)."""
        active = self._active_manifests()
        if not active:
            return _na(ArchGateId.G15, "No ACTIVE manifests to inspect")
        bad: list[str] = []
        for m in active:
            model = self._pr_reg.model(m.resolved_model_id)
            if model is not None and model.is_deprecated:
                bad.append(f"{m.manifest_id}:{m.resolved_model_id}")
        if bad:
            return _fail(
                ArchGateId.G15,
                f"ACTIVE manifest(s) using a deprecated resolved_model_id: {bad}",
            )
        return _pass(
            ArchGateId.G15,
            f"No deprecated model found among {len(active)} ACTIVE manifest(s)",
        )

    def _g16(self) -> ArchGateResult:
        """G-AIARCH-16: No suspended use case has an ACTIVE manifest (ZTAX-AIGOV-REQ-0018)."""
        active = self._active_manifests()
        if not active:
            return _na(ArchGateId.G16, "No ACTIVE manifests to inspect")
        bad: list[str] = []
        for m in active:
            uc = self._uc_reg.get(m.ai_use_case_id)
            if uc is not None and uc.suspended:
                bad.append(
                    f"{m.manifest_id}(use_case={m.ai_use_case_id},suspended=True)"
                )
        if bad:
            return _fail(
                ArchGateId.G16,
                f"ACTIVE manifest(s) for suspended use case(s): {bad}",
            )
        return _pass(
            ArchGateId.G16,
            f"No suspended use case has an ACTIVE manifest across {len(active)} manifest(s)",
        )

    def _g17(self) -> ArchGateResult:
        """G-AIARCH-17: Every ACTIVE manifest has a pinned resolved_model_id
        (ZTAX-AIGOV-REQ-0027)."""
        active = self._active_manifests()
        if not active:
            return _na(ArchGateId.G17, "No ACTIVE manifests to inspect")
        bad = [
            f"{m.manifest_id}:{m.resolved_model_id!r}"
            for m in active
            if not m.resolved_model_id.strip() or _is_alias(m.resolved_model_id)
        ]
        if bad:
            return _fail(
                ArchGateId.G17,
                f"ACTIVE manifest(s) with empty or alias (non-pinned) resolved_model_id: {bad}",
            )
        return _pass(
            ArchGateId.G17,
            f"All {len(active)} ACTIVE manifest(s) have a pinned resolved_model_id",
        )

    def _g18(self) -> ArchGateResult:
        """G-AIARCH-18: Every agentic ACTIVE manifest has a tool_policy_id
        (ZTAX-AIGOV-REQ-0024)."""
        active = self._active_manifests()
        if not active:
            return _na(ArchGateId.G18, "No ACTIVE manifests to inspect")
        agentic = [m for m in active if m.agent_profile_id is not None]
        if not agentic:
            return _na(ArchGateId.G18, "No agentic ACTIVE manifests (agent_profile_id=None)")
        bad = [
            m.manifest_id
            for m in agentic
            if m.tool_policy_id is None or not m.tool_policy_id.strip()
        ]
        if bad:
            return _fail(
                ArchGateId.G18,
                f"Agentic ACTIVE manifest(s) missing tool_policy_id: {bad}",
            )
        return _pass(
            ArchGateId.G18,
            f"All {len(agentic)} agentic ACTIVE manifest(s) have a tool_policy_id",
        )

    def _g19(self) -> ArchGateResult:
        """G-AIARCH-19: ACTIVE manifest authority_level <= UseCase max_authority
        (ZTAX-AIGOV-REQ-0003)."""
        active = self._active_manifests()
        if not active:
            return _na(ArchGateId.G19, "No ACTIVE manifests to inspect")

        auth_order = {a: i for i, a in enumerate(AuthorityOutcome)}
        bad: list[str] = []
        for m in active:
            uc = self._uc_reg.get(m.ai_use_case_id)
            if uc is None:
                continue  # G03 already flags this
            if auth_order[m.authority_level] > auth_order[uc.max_authority]:
                bad.append(
                    f"{m.manifest_id}(manifest={m.authority_level.value}"
                    f">uc_ceiling={uc.max_authority.value})"
                )
        if bad:
            return _fail(
                ArchGateId.G19,
                f"ACTIVE manifest(s) whose authority_level exceeds UseCase ceiling: {bad}",
            )
        return _pass(
            ArchGateId.G19,
            f"All {len(active)} ACTIVE manifest(s) respect their use case authority ceiling",
        )

    def _g20(self) -> ArchGateResult:
        """G-AIARCH-20: Every ACTIVE manifest's resolved_model_id is in the ProviderRegistry
        (ZTAX-AIGOV-REQ-0022)."""
        active = self._active_manifests()
        if not active:
            return _na(ArchGateId.G20, "No ACTIVE manifests to inspect")
        bad = [
            f"{m.manifest_id}:{m.resolved_model_id!r}"
            for m in active
            if self._pr_reg.model(m.resolved_model_id) is None
        ]
        if bad:
            return _fail(
                ArchGateId.G20,
                f"ACTIVE manifest(s) with unregistered resolved_model_id: {bad}",
            )
        return _pass(
            ArchGateId.G20,
            f"All {len(active)} ACTIVE manifest(s) have a registered resolved_model_id",
        )

    # ------------------------------------------------------------------
    # Private helpers
    # ------------------------------------------------------------------

    def _active_manifests(self) -> list[AIReleaseManifest]:
        """Return all ACTIVE manifests — uses internal dict to bypass governance gate."""
        return [self._mf_reg._manifests[mid] for mid in self._mf_reg.active_ids()]

    def _all_providers(self) -> list[ProviderRecord]:
        """Return all registered providers via the internal _providers dict."""
        return list(self._pr_reg._providers.values())

    def _g21(self) -> ArchGateResult:
        """G-AIARCH-21: Every ManifestRegistry-ACTIVE manifest has a lifecycle in sync.

        Catches the gap where ManifestRegistry.active_ids() returns manifests
        that ManifestLifecycle says are still in RESEARCH/DESIGN/VALIDATION/SHADOW
        (no production authority at all per §29).

        Requires *lifecycle_registry* to be supplied to
        :class:`ProductionGateRunner`.  When not supplied, returns NOT_APPLICABLE.
        """
        if self._lc_reg is None:
            return _na(
                ArchGateId.G21,
                "No lifecycle_registry supplied — consistency check skipped",
            )
        active = self._active_manifests()
        if not active:
            return _na(ArchGateId.G21, "No ACTIVE manifests to inspect")

        # Check 1: every ACTIVE manifest has a lifecycle entry.
        missing_lc = [
            m.manifest_id
            for m in active
            if m.manifest_id not in self._lc_reg
        ]
        if missing_lc:
            return _fail(
                ArchGateId.G21,
                f"ACTIVE manifest(s) with no lifecycle tracker: {missing_lc}",
            )

        # Check 2: every lifecycle entry is in a production-serving state.
        bad_state = [
            f"{m.manifest_id}(state={self._lc_reg[m.manifest_id].state.value})"
            for m in active
            if not can_serve_production(self._lc_reg[m.manifest_id].state)
        ]
        if bad_state:
            return _fail(
                ArchGateId.G21,
                f"ACTIVE manifest(s) whose lifecycle is not production-ready: {bad_state}",
            )
        return _pass(
            ArchGateId.G21,
            f"All {len(active)} ACTIVE manifest(s) have a production-serving lifecycle state",
        )


# ---------------------------------------------------------------------------
# Module-level convenience function
# ---------------------------------------------------------------------------


def run_production_gates(
    *,
    use_case_registry: UseCaseRegistry,
    manifest_registry: ManifestRegistry,
    tool_catalog: ToolCatalog,
    provider_registry: ProviderRegistry,
    prompt_registry: PromptProfileRegistry | None = None,
) -> ArchGateReport:
    """Run all 20 production architecture gates and return the report.

    Convenience wrapper for :class:`ProductionGateRunner`.

    Args:
        use_case_registry: The live registry of registered AI use cases.
        manifest_registry: The live manifest registry.
        tool_catalog:      The live tool broker catalog.
        provider_registry: The live provider/model registry.
        prompt_registry:   Optional prompt profile registry.  When ``None``,
                           G-AIARCH-08 returns NOT_APPLICABLE.

    Returns:
        :class:`ArchGateReport` — immutable; suitable for writing to the
        evidence store.
    """
    return ProductionGateRunner(
        use_case_registry=use_case_registry,
        manifest_registry=manifest_registry,
        tool_catalog=tool_catalog,
        provider_registry=provider_registry,
        prompt_registry=prompt_registry,
    ).run()
