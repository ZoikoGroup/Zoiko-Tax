"""Declaration tests for REQ-0027, REQ-0054 and REQ-0058.

Each test below asserts the *literal words* of one requirement against a
source line that genuinely enforces it -- not an adjacent behaviour, and not
a control this file invents. Where the literal statement is only partly
enforced, only the enforced part is tested here; the gap is left for the
requirements audit to report, not silently patched over by a passing test
that doesn't actually prove the missing half.

ZTAX-AIGOV-REQ-0027 (docs/requirements.yaml):
    "Provider aliases such as 'latest' MUST NOT govern material behavior
    without qualification controls."
    Proven here for ``ModelProfile.model_version`` only
    (production_registries.py ``ModelProfile.__post_init__``, which rejects
    "latest"/"default"/"stable"/"preview"). ``PromptProfile.version`` has no
    alias check at all -- only an empty-string check -- which is a real gap,
    not tested in this file (see the requirements audit output, section D).

ZTAX-AIGOV-REQ-0054:
    "AI tool calls MUST be independently authorized."
    Enforced by ``governance.authorise`` and ``tool_broker.authorise`` being
    two separate functions over two separate registries (``UseCaseRegistry``
    / ``ToolCatalog``) with zero call-path coupling between them (confirmed
    by grep: tool_broker.py never imports governance.py and vice versa).
    Each can refuse a call the other would permit.

ZTAX-AIGOV-REQ-0058:
    "Agents MUST NOT mint/extend their own privileges or credentials."
    Enforced by ``ToolCatalog.register`` refusing to admit a PRIVILEGED-class
    tool at all (production_registries.py has no such tool-admission path of
    its own; the broker is the sole admission point), and independently by
    ``tool_broker.authorise`` refusing a PRIVILEGED call even if a privileged
    tool were already present in the catalog (defence in depth -- the second
    check is only reachable by bypassing the public API, which the second
    test does deliberately to prove the call-time refusal does not depend on
    the registration-time refusal).
"""

from __future__ import annotations

from datetime import datetime

import pytest

from ztax_gateway import (
    AuthorityOutcome,
    GovernanceRefusedError,
    Provenance,
    RiskTier,
    UseCase,
    UseCaseRegistry,
)
from ztax_gateway import authorise as governance_authorise
from ztax_gateway.production_registries import (
    Modality,
    ModelHosting,
    ModelProfile,
    ModelProfileError,
)
from ztax_gateway.tool_broker import (
    ActionClass,
    ToolBrokerRefusedError,
    ToolCatalog,
    ToolProfile,
    ToolProvenance,
)
from ztax_gateway.tool_broker import Refusal as ToolRefusal
from ztax_gateway.tool_broker import authorise as tool_authorise

_TS = datetime(2026, 9, 22, 12, 0, 0)


def _make_model_profile(**overrides: object) -> ModelProfile:
    defaults: dict[str, object] = dict(
        model_profile_id="model:classifier",
        owner="lane-l",
        version="1.0.0",
        provider_profile_id="provider:eu-hosted",
        model_id="classifier",
        model_version="2026-09-01",
        hosting=ModelHosting.PRIVATE_CLOUD,
        context_window_tokens=128_000,
        max_output_tokens=4096,
        modalities=(Modality.TEXT,),
        tool_capable=False,
        permitted_regions=frozenset({"eu-west-1"}),
        permitted_data_classes=frozenset({"P0", "P1"}),
        terms_ref="contract:2026-001",
        known_limitations=(),
        deprecation_date=None,
        suspended=False,
    )
    defaults.update(overrides)
    return ModelProfile(**defaults)  # type: ignore[arg-type]


# ---------------------------------------------------------------------------
# REQ-0027 -- provider aliases must not govern material behavior
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("alias", ["latest", "default", "stable", "preview"])
def test_req_0027_model_version_rejects_floating_alias(alias: str) -> None:
    """REQ-0027: a ModelProfile declaring a floating alias as its
    model_version is refused -- the model version actually served can never
    be the undeclared, silently-moving thing an alias names.

    Enforced at production_registries.py: ``ModelProfile.__post_init__``,
    ``if self.model_version in _MODEL_VERSION_ALIASES: raise ModelProfileError``.
    """
    with pytest.raises(ModelProfileError, match="floating alias"):
        _make_model_profile(model_version=alias)


# ---------------------------------------------------------------------------
# REQ-0054 -- AI tool calls MUST be independently authorized
# ---------------------------------------------------------------------------

_PERMITTING_USE_CASE = UseCase(
    use_case_id="classification-review",
    owner="lane-l",
    description="Operator-initiated classification review",
    max_risk_tier=RiskTier.T2,
    max_authority=AuthorityOutcome.A1,
    permitted_regions=frozenset({"eu-west-1"}),
    permitted_data_classes=frozenset({"P0"}),
)

_PERMITTING_GOVERNANCE_PROVENANCE = Provenance(
    use_case="classification-review",
    model_profile="model:review@2026.03",
    provider_profile="provider:eu-hosted",
    prompt_profile="prompt:classification-review@4",
    region="eu-west-1",
    data_class="P0",
    risk_tier=RiskTier.T1,
    authority_outcome=AuthorityOutcome.A1,
    ai_train_version="ai-2027.03.1",
)

_PERMITTING_TOOL = ToolProfile(
    tool_id="invoice-extractor",
    owner="lane-l",
    description="Extract line-items from an invoice.",
    action_class=ActionClass.READ,
    required_scopes=frozenset(),
    idempotent=False,
)

_PERMITTING_TOOL_PROVENANCE = ToolProvenance(
    tool_id="invoice-extractor",
    caller_id="agent:classification-review",
    region="eu-west-1",
    timestamp=_TS,
    scopes=frozenset(),
)


def test_req_0054_tool_broker_refuses_a_call_governance_would_permit() -> None:
    """REQ-0054: the tool broker is its own authorization gate -- it refuses
    a call under governance context that ``governance.authorise`` would
    permit on its own terms, because the tool itself is simply not
    registered in the ``ToolCatalog`` the broker consults.

    Enforced at tool_broker.py: ``authorise`` step 2 (registration), raising
    ``ToolBrokerRefusedError(Refusal.UNKNOWN_TOOL, ...)`` -- a refusal
    governance.py has no knowledge of and cannot see, because
    tool_broker.py never imports governance.py (confirmed by grep: zero
    matches for "governance" as a code reference, only as prose in
    comments/docstrings).
    """
    # The governance gate permits this call on its own terms.
    use_case = governance_authorise(
        UseCaseRegistry([_PERMITTING_USE_CASE]), _PERMITTING_GOVERNANCE_PROVENANCE
    )
    assert use_case.use_case_id == "classification-review"

    # The tool broker, consulted independently for the same call, refuses it
    # -- the empty catalog never registered "invoice-extractor".
    with pytest.raises(ToolBrokerRefusedError) as exc:
        tool_authorise(ToolCatalog([]), _PERMITTING_TOOL_PROVENANCE)
    assert exc.value.refusal == ToolRefusal.UNKNOWN_TOOL


def test_req_0054_governance_refuses_a_call_the_tool_broker_would_permit() -> None:
    """REQ-0054, the reverse direction: governance refuses a call (the use
    case is simply not registered) that the tool broker, consulted
    independently for the identical tool call, permits -- proving neither
    gate's verdict depends on, or is overridden by, the other's.

    Enforced at governance.py: ``authorise`` step 2 (registration), raising
    ``GovernanceRefusedError(Refusal.UNKNOWN_USE_CASE, ...)``, while
    tool_broker.py's ``authorise`` runs to completion and returns the tool.
    """
    with pytest.raises(GovernanceRefusedError):
        governance_authorise(UseCaseRegistry([]), _PERMITTING_GOVERNANCE_PROVENANCE)

    tool = tool_authorise(ToolCatalog([_PERMITTING_TOOL]), _PERMITTING_TOOL_PROVENANCE)
    assert tool.tool_id == "invoice-extractor"


# ---------------------------------------------------------------------------
# REQ-0058 -- agents MUST NOT mint/extend their own privileges
# ---------------------------------------------------------------------------


def test_req_0058_registering_a_privileged_tool_is_refused() -> None:
    """REQ-0058: an attempt to add a PRIVILEGED-class tool to the catalog --
    the only way a new capability enters the tool plane at all -- is
    refused outright. There is no registration path by which a tool (and
    therefore an agent calling it) can mint itself the top privilege tier.

    Enforced at tool_broker.py: ``ToolCatalog.register``,
    ``if tool.action_class == ActionClass.PRIVILEGED: raise ValueError(...)``.
    """
    privileged_tool = ToolProfile(
        tool_id="danger-tool",
        owner="lane-l",
        description="Would be able to do anything.",
        action_class=ActionClass.PRIVILEGED,
        required_scopes=frozenset(),
        idempotent=False,
    )
    with pytest.raises(ValueError, match="PRIVILEGED"):
        ToolCatalog([privileged_tool])


def test_req_0058_calling_a_privileged_tool_is_refused_even_if_already_catalogued() -> None:
    """REQ-0058, the call-time half: even granting a catalog that somehow
    already held a PRIVILEGED tool (bypassing the public ``register`` API
    below, deliberately, to isolate this check), the broker's own authorise
    step still refuses the call unconditionally. The privilege-mint refusal
    does not rely solely on the registration-time check -- a second,
    independent refusal exists at call time.

    Enforced at tool_broker.py: ``authorise`` step 4,
    ``if tool.action_class == ActionClass.PRIVILEGED: raise
    ToolBrokerRefusedError(Refusal.PRIVILEGED_FORBIDDEN, ...)``.
    """
    privileged_tool = ToolProfile(
        tool_id="danger-tool",
        owner="lane-l",
        description="Would be able to do anything.",
        action_class=ActionClass.PRIVILEGED,
        required_scopes=frozenset(),
        idempotent=False,
    )
    catalog = ToolCatalog([])
    # Bypass the public API on purpose -- register() already refuses this
    # tool, so the only way to exercise authorise()'s own PRIVILEGED check in
    # isolation is to place it directly, proving that check does not depend
    # on register() having let anything slip through.
    catalog._tools["danger-tool"] = privileged_tool

    provenance = ToolProvenance(
        tool_id="danger-tool",
        caller_id="agent:classification-review",
        region="eu-west-1",
        timestamp=_TS,
        scopes=frozenset(),
    )
    with pytest.raises(ToolBrokerRefusedError) as exc:
        tool_authorise(catalog, provenance)
    assert exc.value.refusal == ToolRefusal.PRIVILEGED_FORBIDDEN
