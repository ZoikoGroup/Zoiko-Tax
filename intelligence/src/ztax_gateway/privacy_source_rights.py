"""Privacy, Source-Rights & Customer Policy Integration.

Chapter 17 §18 of the ZoikoTax Master Specification, grounded in:

* ZTAX-PRIV-001 (Privacy Engineering) — requirements 0004-0007, 0071-0078
* ZTAX-SRC-001  (Content Sourcing & Licensing) — requirements 0003-0008

This module is the enforcement layer that sits *before* an AI invocation
proceeds.  It answers six questions, each a hard gate with no override path:

1. **PRIV gate** (ZTAX-PRIV-REQ-0004 - 0007):
   Does the ``ProcessingActivity`` and its declared AI-allowed flag permit
   this invocation at all?  A ``ProcessingActivity`` with
   ``ai_allowed=False`` blocks the call regardless of governance authority.

2. **SRC gate** (ZTAX-SRC-REQ-0004, -0005):
   Does the source's ``RightsProfile`` permit the requested use
   (retrieval / embedding / disclosure / evaluation / training)?  ``UNKNOWN``
   resolves as ``DENY`` for every use (ZTAX-SRC-REQ-0005).

3. **Ceiling rule** (ZTAX-PRIV-001 §4):
   Customer contracts can only *restrict*, never *expand*, the privacy/source
   rights floor established by the platform policy.  A ``CustomerPolicy``
   cannot grant rights that the underlying ``RightsProfile`` denies.

4. **Corpus build guard** (ZTAX-PRIV-REQ-0073, ZTAX-SRC-REQ-0004):
   Every source added to a RAG corpus must pass a ``RightsProfile`` check for
   ``USE_RETRIEVAL`` *and* ``USE_EMBEDDING`` before it may be indexed.  A
   source with ``UNKNOWN`` or ``DENY`` rights blocks the corpus build.

5. **Provider retention / no-training monitor** (ZTAX-PRIV-REQ-0071,
   ZTAX-PRIV-REQ-0074):
   Providers must declare ``no_training=True`` before they may receive any
   personal-data class (P2+).  A provider whose retention window exceeds the
   ``ProcessingActivity``'s maximum allowed window is refused.

6. **Customer opt-out routing** (ZTAX-PRIV-REQ-0075, §18):
   When a customer has opted out of non-essential AI, the call is refused with
   ``PrivacyRefusal.CUSTOMER_OPT_OUT`` and the caller is directed to the
   deterministic manual path.

Design rules
------------
* No live model calls.
* No fiscal imports (ADR-0006 §2.6).
* All data-carrying types are ``frozen=True`` dataclasses — immutable evidence.
* The ceiling rule is monotone: intersection only, never union.
* ``RightsDecision.UNKNOWN`` always collapses to ``DENY`` (fail-safe).
* ``check_invocation_privacy()`` and ``check_corpus_source()`` are the two
  primary entry-points; all other helpers are public for testing.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum
from typing import Final

# ---------------------------------------------------------------------------
# Public names
# ---------------------------------------------------------------------------

__all__: list[str] = [
    "CustomerPolicy",
    "DataSubjectCategory",
    "LegalBasis",
    "PrivacyRefusal",
    "PrivacySourceRightsError",
    "ProcessingActivity",
    "ProcessingRole",
    "ProviderRetentionPolicy",
    "RightsDecision",
    "RightsProfile",
    "SourceClass",
    "SourceRightsRecord",
    "SourceUse",
    "check_corpus_source",
    "check_invocation_privacy",
    "check_provider_retention",
    "check_source_use",
]


# ---------------------------------------------------------------------------
# §18 Rule 1 — ProcessingActivity / purpose gate
# (ZTAX-PRIV-REQ-0004, -0005, -0006, -0007)
# ---------------------------------------------------------------------------


class ProcessingRole(StrEnum):
    """Zoiko's role in the processing activity (ZTAX-PRIV-REQ-0004).

    ``CONTROLLER``  -- Zoiko determines the purpose and means; applies when
                       ZoikoTax makes autonomous tax-determination decisions.
    ``PROCESSOR``   -- Zoiko acts on behalf of the customer controller; applies
                       when ZoikoTax processes customer-owned data under a DPA.
    ``JOINT``       -- both parties determine purpose and means jointly.
    """

    CONTROLLER = "CONTROLLER"
    PROCESSOR = "PROCESSOR"
    JOINT = "JOINT"


class LegalBasis(StrEnum):
    """Legal basis for processing (GDPR Art 6 / equivalent).

    ``CONTRACT``            -- necessary for performance of a contract.
    ``LEGAL_OBLIGATION``    -- compliance with a legal obligation.
    ``LEGITIMATE_INTEREST`` -- legitimate interests pursued by the controller.
    ``CONSENT``             -- data subject has given consent.
    ``VITAL_INTEREST``      -- protect vital interests of data subjects.
    ``PUBLIC_TASK``         -- task carried out in the public interest.
    """

    CONTRACT = "CONTRACT"
    LEGAL_OBLIGATION = "LEGAL_OBLIGATION"
    LEGITIMATE_INTEREST = "LEGITIMATE_INTEREST"
    CONSENT = "CONSENT"
    VITAL_INTEREST = "VITAL_INTEREST"
    PUBLIC_TASK = "PUBLIC_TASK"


class DataSubjectCategory(StrEnum):
    """Categories of data subjects covered by the activity (ZTAX-PRIV-REQ-0005).

    ``SUBSCRIBER``  -- end-customer / subscriber of the telco.
    ``EMPLOYEE``    -- Zoiko or customer employee.
    ``BUSINESS``    -- corporate customer entity.
    ``THIRD_PARTY`` -- any other identified or identifiable natural person.
    """

    SUBSCRIBER = "SUBSCRIBER"
    EMPLOYEE = "EMPLOYEE"
    BUSINESS = "BUSINESS"
    THIRD_PARTY = "THIRD_PARTY"


@dataclass(frozen=True, slots=True)
class ProcessingActivity:
    """One registered AI processing activity (ZTAX-PRIV-REQ-0004 - 0007).

    ``activity_id``             -- stable identifier for this activity.
    ``purpose``                 -- human-readable purpose statement. Non-empty.
    ``role``                    -- Zoiko's processing role.
    ``legal_basis``             -- legal basis for the activity.
    ``data_subject_categories`` -- data subject categories covered
                                   (ZTAX-PRIV-REQ-0005).  Must be non-empty.
    ``permitted_regions``       -- approved storage/processing regions
                                   (ZTAX-PRIV-REQ-0006).  Empty = none permitted.
    ``ai_allowed``              -- ``True`` when AI is explicitly permitted for
                                   this activity.  ``False`` blocks all AI calls.
    ``max_retention_days``      -- maximum allowed provider retention (days);
                                   ``0`` = transient only.
    """

    activity_id: str
    purpose: str
    role: ProcessingRole
    legal_basis: LegalBasis
    data_subject_categories: frozenset[DataSubjectCategory]
    permitted_regions: frozenset[str]
    ai_allowed: bool
    max_retention_days: int

    def __post_init__(self) -> None:
        if not self.activity_id:
            raise PrivacySourceRightsError(
                "privacy: ProcessingActivity.activity_id must be non-empty"
            )
        if not self.purpose:
            raise PrivacySourceRightsError(
                f"privacy: ProcessingActivity {self.activity_id!r} must declare a purpose"
            )
        if not self.data_subject_categories:
            raise PrivacySourceRightsError(
                f"privacy: ProcessingActivity {self.activity_id!r} must name at least one "
                "data subject category (ZTAX-PRIV-REQ-0005)"
            )
        if self.max_retention_days < 0:
            raise PrivacySourceRightsError(
                f"privacy: ProcessingActivity {self.activity_id!r} max_retention_days "
                "must be >= 0 (use 0 for transient/no-retention)"
            )

    def permits_region(self, region: str) -> bool:
        """Return ``True`` when *region* is in the permitted regions list."""
        return region in self.permitted_regions


# ---------------------------------------------------------------------------
# §18 Rule 2 — Source rights gate
# (ZTAX-SRC-REQ-0003, -0004, -0005, -0006)
# ---------------------------------------------------------------------------


class SourceClass(StrEnum):
    """Source class S0-S7 as defined by ZTAX-SRC-001 (ZTAX-SRC-REQ-0003).

    ``S0`` -- primary binding law (statutes, official regulations).
    ``S1`` -- official administrative guidance (rulings, circulars).
    ``S2`` -- treaty / international instrument.
    ``S3`` -- licensed commercial content (premium databases).
    ``S4`` -- open-access published research.
    ``S5`` -- customer-provided reference material.
    ``S6`` -- internal Zoiko analysis / documentation.
    ``S7`` -- unclassified / discovery web (lowest trust).
    """

    S0 = "S0"
    S1 = "S1"
    S2 = "S2"
    S3 = "S3"
    S4 = "S4"
    S5 = "S5"
    S6 = "S6"
    S7 = "S7"


class SourceUse(StrEnum):
    """The specific use a RightsProfile must permit (ZTAX-SRC-REQ-0004).

    Each use is evaluated *independently* — a PERMIT for retrieval does not
    imply a PERMIT for embedding or training.

    ``USE_RETRIEVAL``  -- chunk retrieval at query time (RAG search).
    ``USE_EMBEDDING``  -- generating vector embeddings for indexing.
    ``USE_DISCLOSURE`` -- including source text in a model prompt or output
                          that may be seen by a third party (subprocessor).
    ``USE_EVALUATION`` -- using source text in evaluation / test sets.
    ``USE_TRAINING``   -- including source text in model fine-tuning or
                          pre-training datasets.
    """

    USE_RETRIEVAL = "USE_RETRIEVAL"
    USE_EMBEDDING = "USE_EMBEDDING"
    USE_DISCLOSURE = "USE_DISCLOSURE"
    USE_EVALUATION = "USE_EVALUATION"
    USE_TRAINING = "USE_TRAINING"


class RightsDecision(StrEnum):
    """Per-use rights verdict (ZTAX-SRC-REQ-0005).

    ``PERMIT``  -- the rights holder has explicitly permitted this use.
    ``DENY``    -- the rights holder has explicitly denied this use.
    ``UNKNOWN`` -- rights status has not been assessed; resolves as DENY
                   for any production execution or corpus build.
    """

    PERMIT = "PERMIT"
    DENY = "DENY"
    UNKNOWN = "UNKNOWN"


# The definitive set of all evaluated uses, used to validate completeness.
_ALL_USES: Final[frozenset[SourceUse]] = frozenset(SourceUse)


@dataclass(frozen=True, slots=True)
class RightsProfile:
    """Per-source content rights declaration (ZTAX-SRC-REQ-0004).

    Each of the five :class:`SourceUse` values must have an explicit
    :class:`RightsDecision`.  ``UNKNOWN`` always collapses to ``DENY`` when
    evaluated (ZTAX-SRC-REQ-0005).

    ``source_id``    -- matches the ``source_id`` on a RAG Chunk.
    ``source_class`` -- :class:`SourceClass` S0-S7.
    ``decisions``    -- mapping from :class:`SourceUse` to :class:`RightsDecision`.
                        All five uses must be present.
    ``licence_spdx`` -- SPDX licence expression or ``"LicenseRef-..."``
                        for non-SPDX licences.  Non-empty.
    """

    source_id: str
    source_class: SourceClass
    decisions: dict[SourceUse, RightsDecision]
    licence_spdx: str

    def __post_init__(self) -> None:
        if not self.source_id:
            raise PrivacySourceRightsError(
                "privacy: RightsProfile.source_id must be non-empty"
            )
        if not self.licence_spdx:
            raise PrivacySourceRightsError(
                f"privacy: RightsProfile {self.source_id!r} must declare a licence_spdx "
                "(use 'LicenseRef-...' for non-SPDX licences)"
            )
        missing = _ALL_USES - frozenset(self.decisions)
        if missing:
            raise PrivacySourceRightsError(
                f"privacy: RightsProfile {self.source_id!r} is missing decisions for: "
                + ", ".join(sorted(m.value for m in missing))
            )

    def permits(self, use: SourceUse) -> bool:
        """Return ``True`` only when the decision is explicitly ``PERMIT``.

        ``UNKNOWN`` collapses to ``DENY`` (ZTAX-SRC-REQ-0005): fail-safe.
        """
        return self.decisions.get(use) is RightsDecision.PERMIT


# ---------------------------------------------------------------------------
# §18 Rule 3 — Customer policy ceiling
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class CustomerPolicy:
    """Customer-level policy overlay (Chapter 17 §18 Rule 3).

    A customer contract can *restrict* but never *expand* the platform's
    privacy/source rights.  The effective decision for any use is:

        ``RightsDecision.DENY`` if either the platform rights or the customer
        policy deny; ``RightsDecision.PERMIT`` only if both permit.

    ``customer_id``        -- stable identifier for the customer/tenant.
    ``ai_opted_out``       -- ``True`` when the customer has opted out of all
                              non-essential AI processing; triggers
                              ``PrivacyRefusal.CUSTOMER_OPT_OUT``.
    ``denied_uses``        -- set of :class:`SourceUse` the customer additionally
                              restricts for their data.
    ``max_retention_days`` -- customer-imposed maximum provider retention (days);
                              ``None`` means no tighter limit than the
                              ``ProcessingActivity``.
    """

    customer_id: str
    ai_opted_out: bool = False
    denied_uses: frozenset[SourceUse] = field(default_factory=frozenset)
    max_retention_days: int | None = None

    def __post_init__(self) -> None:
        if not self.customer_id:
            raise PrivacySourceRightsError(
                "privacy: CustomerPolicy.customer_id must be non-empty"
            )
        if self.max_retention_days is not None and self.max_retention_days < 0:
            raise PrivacySourceRightsError(
                f"privacy: CustomerPolicy {self.customer_id!r} max_retention_days "
                "must be >= 0 if set"
            )

    def effective_permits(self, profile: RightsProfile, use: SourceUse) -> bool:
        """Return ``True`` only when both the RightsProfile *and* this policy permit *use*.

        The ceiling rule: customer contracts can only restrict, never expand.
        (Chapter 17 §18 Rule 3)
        """
        if use in self.denied_uses:
            return False
        return profile.permits(use)

    def effective_max_retention_days(self, activity_max: int) -> int:
        """Return the effective maximum retention days — the tighter of both limits."""
        if self.max_retention_days is None:
            return activity_max
        return min(self.max_retention_days, activity_max)


# ---------------------------------------------------------------------------
# §18 Rule 5 — Provider retention / no-training monitor
# (ZTAX-PRIV-REQ-0071, -0074)
# ---------------------------------------------------------------------------

# Data class prefixes that indicate personal data (P2 and above).
_PERSONAL_DATA_PREFIXES: Final[tuple[str, ...]] = ("P2", "P3", "P4", "P5", "P6")


@dataclass(frozen=True, slots=True)
class ProviderRetentionPolicy:
    """Declared retention and training policy for one AI provider (§18 Rule 5).

    ``provider_id``    -- matches a ProviderRecord.provider_id.
    ``no_training``    -- ``True`` when the provider commits to not training on
                          customer/source personal data (ZTAX-PRIV-REQ-0071).
                          Personal-data class (P2+) invocations are refused if
                          this is ``False``.
    ``retention_days`` -- how many days the provider retains inference inputs/
                          outputs.  ``0`` = transient (no retention).
    """

    provider_id: str
    no_training: bool
    retention_days: int

    def __post_init__(self) -> None:
        if not self.provider_id:
            raise PrivacySourceRightsError(
                "privacy: ProviderRetentionPolicy.provider_id must be non-empty"
            )
        if self.retention_days < 0:
            raise PrivacySourceRightsError(
                f"privacy: ProviderRetentionPolicy {self.provider_id!r} retention_days "
                "must be >= 0 (use 0 for transient/no-retention)"
            )


# ---------------------------------------------------------------------------
# Audit record for source-rights decisions
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class SourceRightsRecord:
    """Immutable audit record for a source-rights check (§18 Rules 2 & 3).

    Written to the Evidence Ledger so every corpus inclusion decision is
    traceable (ZTAX-PRIV-REQ-0073, ZTAX-SRC-REQ-0004).

    ``source_id``   -- the source evaluated.
    ``use``         -- the :class:`SourceUse` evaluated.
    ``decision``    -- effective rights decision (after customer ceiling).
    ``customer_id`` -- the customer policy applied; ``""`` if none.
    ``rationale``   -- human-readable reason for the decision.
    """

    source_id: str
    use: SourceUse
    decision: RightsDecision
    customer_id: str
    rationale: str


# ---------------------------------------------------------------------------
# Error type
# ---------------------------------------------------------------------------


class PrivacyRefusal(StrEnum):
    """Refusal codes for privacy and source-rights gates.

    These strings appear in logs, metrics, and error responses.  Closed
    vocabulary — same discipline as :class:`~ztax_gateway.governance.Refusal`.

    ``ACTIVITY_AI_BLOCKED``
        ``ProcessingActivity.ai_allowed`` is ``False``.
    ``ACTIVITY_REGION_DENIED``
        The invocation's region is not in the activity's ``permitted_regions``.
    ``CUSTOMER_OPT_OUT``
        The customer has opted out of non-essential AI (§18 Rule 6);
        caller must route to the deterministic/manual path.
    ``SOURCE_RIGHTS_DENIED``
        The ``RightsProfile`` decision is DENY or UNKNOWN for the requested use
        (ZTAX-SRC-REQ-0005).
    ``CUSTOMER_POLICY_DENIED``
        The ``CustomerPolicy`` additionally denies the requested use (§18 Rule 3).
    ``PROVIDER_TRAINS_ON_PERSONAL_DATA``
        The provider does not carry ``no_training=True`` but data class is P2+
        (ZTAX-PRIV-REQ-0071).
    ``PROVIDER_RETENTION_EXCEEDED``
        The provider's ``retention_days`` exceeds the effective maximum allowed
        by the ``ProcessingActivity`` + ``CustomerPolicy``
        (ZTAX-PRIV-REQ-0074).
    """

    ACTIVITY_AI_BLOCKED = "PRIVACY_ACTIVITY_AI_BLOCKED"
    ACTIVITY_REGION_DENIED = "PRIVACY_ACTIVITY_REGION_DENIED"
    CUSTOMER_OPT_OUT = "PRIVACY_CUSTOMER_OPT_OUT"
    SOURCE_RIGHTS_DENIED = "PRIVACY_SOURCE_RIGHTS_DENIED"
    CUSTOMER_POLICY_DENIED = "PRIVACY_CUSTOMER_POLICY_DENIED"
    PROVIDER_TRAINS_ON_PERSONAL_DATA = "PRIVACY_PROVIDER_TRAINS_ON_PERSONAL_DATA"
    PROVIDER_RETENTION_EXCEEDED = "PRIVACY_PROVIDER_RETENTION_EXCEEDED"


class PrivacySourceRightsError(Exception):
    """Raised when a privacy or source-rights gate is breached.

    Carries a :class:`PrivacyRefusal` code and a human-readable detail
    string.  The error message is ``"{code}: {detail}"`` so structured log
    parsers can extract the code without string parsing.
    """

    def __init__(self, detail: str, refusal: PrivacyRefusal | None = None) -> None:
        code = refusal.value if refusal else "PRIVACY_SOURCE_RIGHTS_ERROR"
        super().__init__(f"{code}: {detail}")
        self.refusal = refusal
        self.detail = detail


# ---------------------------------------------------------------------------
# Primary gate 1: invocation privacy check (§18 Rules 1 + 6)
# ---------------------------------------------------------------------------


def check_invocation_privacy(
    activity: ProcessingActivity,
    region: str,
    *,
    customer_policy: CustomerPolicy | None = None,
) -> None:
    """Gate an AI invocation against the processing activity and customer policy.

    Implements §18 Rules 1 and 6.  Must be called *before* any model
    invocation proceeds.

    Order of checks (decreasing blast radius — same discipline as
    :func:`~ztax_gateway.governance.authorise`):

    1. **Customer opt-out** (§18 Rule 6) — refuse immediately with
       :attr:`PrivacyRefusal.CUSTOMER_OPT_OUT`.
    2. **Activity AI-allowed flag** (ZTAX-PRIV-REQ-0004) — refuse with
       :attr:`PrivacyRefusal.ACTIVITY_AI_BLOCKED`.
    3. **Region check** (ZTAX-PRIV-REQ-0006) — refuse with
       :attr:`PrivacyRefusal.ACTIVITY_REGION_DENIED`.

    Args:
        activity:        The registered :class:`ProcessingActivity` for this call.
        region:          The region the invocation is executing in.
        customer_policy: Optional customer-level overlay.

    Raises:
        PrivacySourceRightsError: with the appropriate :class:`PrivacyRefusal`
            code when any gate is breached.
    """
    # 1. Customer opt-out (§18 Rule 6) — checked first, widest blast radius.
    if customer_policy is not None and customer_policy.ai_opted_out:
        raise PrivacySourceRightsError(
            f"customer {customer_policy.customer_id!r} has opted out of non-essential AI; "
            "route to the deterministic/manual path",
            PrivacyRefusal.CUSTOMER_OPT_OUT,
        )

    # 2. Activity AI-allowed flag.
    if not activity.ai_allowed:
        raise PrivacySourceRightsError(
            f"ProcessingActivity {activity.activity_id!r} does not permit AI processing "
            f"(purpose: {activity.purpose!r})",
            PrivacyRefusal.ACTIVITY_AI_BLOCKED,
        )

    # 3. Region check (ZTAX-PRIV-REQ-0006).
    if not activity.permits_region(region):
        raise PrivacySourceRightsError(
            f"region {region!r} is not in the permitted regions for "
            f"ProcessingActivity {activity.activity_id!r}",
            PrivacyRefusal.ACTIVITY_REGION_DENIED,
        )


# ---------------------------------------------------------------------------
# Primary gate 2: source use rights check (§18 Rules 2 + 3)
# ---------------------------------------------------------------------------


def check_source_use(
    profile: RightsProfile,
    use: SourceUse,
    *,
    customer_policy: CustomerPolicy | None = None,
) -> SourceRightsRecord:
    """Gate a specific use of a source against its RightsProfile and optional customer policy.

    Implements §18 Rules 2 and 3.

    Decision order:

    1. **Platform RightsProfile** (ZTAX-SRC-REQ-0004, -0005):
       ``UNKNOWN`` collapses to ``DENY``.
    2. **Customer policy ceiling** (§18 Rule 3): customer can only restrict.

    Args:
        profile:         The :class:`RightsProfile` for the source.
        use:             The :class:`SourceUse` being requested.
        customer_policy: Optional customer-level override.

    Returns:
        :class:`SourceRightsRecord` documenting the ``PERMIT`` decision.

    Raises:
        PrivacySourceRightsError: with :attr:`PrivacyRefusal.SOURCE_RIGHTS_DENIED`
            if the platform profile denies/UNKNOWN, or
            :attr:`PrivacyRefusal.CUSTOMER_POLICY_DENIED` if customer additionally
            denies.
    """
    customer_id = customer_policy.customer_id if customer_policy else ""

    # 1. Platform rights check.
    raw = profile.decisions.get(use, RightsDecision.UNKNOWN)
    if raw is not RightsDecision.PERMIT:
        raise PrivacySourceRightsError(
            f"source {profile.source_id!r}: platform rights decision is {raw.value} "
            f"for use {use.value}; UNKNOWN resolves as DENY (ZTAX-SRC-REQ-0005)",
            PrivacyRefusal.SOURCE_RIGHTS_DENIED,
        )

    # 2. Customer policy ceiling (§18 Rule 3).
    if customer_policy is not None and use in customer_policy.denied_uses:
        raise PrivacySourceRightsError(
            f"source {profile.source_id!r}: customer {customer_id!r} policy denies "
            f"{use.value} on top of the platform rights profile (§18 Rule 3)",
            PrivacyRefusal.CUSTOMER_POLICY_DENIED,
        )

    return SourceRightsRecord(
        source_id=profile.source_id,
        use=use,
        decision=RightsDecision.PERMIT,
        customer_id=customer_id,
        rationale="platform PERMIT; customer policy does not additionally restrict",
    )


# ---------------------------------------------------------------------------
# Primary gate 3: corpus build / RAG source check (§18 Rule 4)
# (ZTAX-PRIV-REQ-0073, ZTAX-SRC-REQ-0004)
# ---------------------------------------------------------------------------


def check_corpus_source(
    profile: RightsProfile,
    *,
    customer_policy: CustomerPolicy | None = None,
) -> tuple[SourceRightsRecord, SourceRightsRecord]:
    """Gate a source for inclusion in a RAG corpus.

    Implements §18 Rule 4.  A source may only be indexed if its
    :class:`RightsProfile` explicitly permits *both* ``USE_RETRIEVAL`` and
    ``USE_EMBEDDING``.  Either DENY or UNKNOWN on either use blocks the build.

    Both checks must pass before the source may be handed to
    :meth:`~ztax_gateway.rag.KnowledgeBase.build`.

    Args:
        profile:         :class:`RightsProfile` for the candidate source.
        customer_policy: Optional customer-level policy.

    Returns:
        A 2-tuple ``(retrieval_record, embedding_record)`` of
        :class:`SourceRightsRecord` instances, both with decision ``PERMIT``.

    Raises:
        PrivacySourceRightsError: if either ``USE_RETRIEVAL`` or
            ``USE_EMBEDDING`` is not permitted.
    """
    retrieval_record = check_source_use(
        profile, SourceUse.USE_RETRIEVAL, customer_policy=customer_policy
    )
    embedding_record = check_source_use(
        profile, SourceUse.USE_EMBEDDING, customer_policy=customer_policy
    )
    return retrieval_record, embedding_record


# ---------------------------------------------------------------------------
# Primary gate 4: provider retention / no-training check (§18 Rule 5)
# (ZTAX-PRIV-REQ-0071, -0074)
# ---------------------------------------------------------------------------


def _is_personal_data_class(data_class: str) -> bool:
    """Return ``True`` when *data_class* indicates personal data (P2 or above)."""
    return any(data_class.upper().startswith(p) for p in _PERSONAL_DATA_PREFIXES)


def check_provider_retention(
    provider_policy: ProviderRetentionPolicy,
    activity: ProcessingActivity,
    data_class: str,
    *,
    customer_policy: CustomerPolicy | None = None,
) -> None:
    """Gate an AI provider against retention and no-training requirements.

    Implements §18 Rule 5.

    Checks (in order):

    1. If *data_class* is personal (P2+) and ``provider_policy.no_training``
       is ``False``, refuse with
       :attr:`PrivacyRefusal.PROVIDER_TRAINS_ON_PERSONAL_DATA`
       (ZTAX-PRIV-REQ-0071).
    2. If ``provider_policy.retention_days`` exceeds the effective maximum
       (the tighter of ``activity.max_retention_days`` and
       ``customer_policy.max_retention_days``), refuse with
       :attr:`PrivacyRefusal.PROVIDER_RETENTION_EXCEEDED`
       (ZTAX-PRIV-REQ-0074).

    Args:
        provider_policy:  Declared retention policy for the AI provider.
        activity:         The active :class:`ProcessingActivity`.
        data_class:       The data-class label on the invocation (e.g. ``"P3"``).
        customer_policy:  Optional customer-level policy; may impose a tighter
                          retention ceiling.

    Raises:
        PrivacySourceRightsError: with the appropriate
            :class:`PrivacyRefusal` code.
    """
    # 1. No-training gate (ZTAX-PRIV-REQ-0071).
    if _is_personal_data_class(data_class) and not provider_policy.no_training:
        raise PrivacySourceRightsError(
            f"provider {provider_policy.provider_id!r} does not carry no_training=True "
            f"but data_class {data_class!r} is personal (P2+); "
            "violates ZTAX-PRIV-REQ-0071",
            PrivacyRefusal.PROVIDER_TRAINS_ON_PERSONAL_DATA,
        )

    # 2. Retention window gate (ZTAX-PRIV-REQ-0074).
    effective_max = (
        customer_policy.effective_max_retention_days(activity.max_retention_days)
        if customer_policy is not None
        else activity.max_retention_days
    )
    if provider_policy.retention_days > effective_max:
        raise PrivacySourceRightsError(
            f"provider {provider_policy.provider_id!r} retention_days "
            f"({provider_policy.retention_days}) exceeds effective maximum "
            f"({effective_max}) for ProcessingActivity {activity.activity_id!r}; "
            "violates ZTAX-PRIV-REQ-0074",
            PrivacyRefusal.PROVIDER_RETENTION_EXCEEDED,
        )
