"""Tests for privacy_source_rights.py (Chapter 17 §18).

Coverage
--------
Every rule from §18 has a dedicated section:

Rule 1  -- ProcessingActivity gate: ai_allowed, region check
Rule 2  -- RightsProfile / UNKNOWN=DENY, per-use independence
Rule 3  -- Customer ceiling: can only restrict, never expand
Rule 4  -- Corpus build guard: both USE_RETRIEVAL and USE_EMBEDDING required
Rule 5  -- Provider no_training gate (P2+) and retention_days ceiling
Rule 6  -- Customer opt-out → CUSTOMER_OPT_OUT refusal

Construction invariants:
  - empty activity_id / purpose / data_subject_categories → error
  - negative max_retention_days → error
  - missing RightsProfile decisions → error
  - empty customer_id → error
  - negative ProviderRetentionPolicy.retention_days → error

PrivacySourceRightsError:
  - carries .refusal and .detail attributes
  - message starts with the refusal code
"""

from __future__ import annotations

import pytest

from ztax_gateway.privacy_source_rights import (
    CustomerPolicy,
    DataSubjectCategory,
    LegalBasis,
    PrivacyRefusal,
    PrivacySourceRightsError,
    ProcessingActivity,
    ProcessingRole,
    ProviderRetentionPolicy,
    RightsDecision,
    RightsProfile,
    SourceClass,
    SourceRightsRecord,
    SourceUse,
    check_corpus_source,
    check_invocation_privacy,
    check_provider_retention,
    check_source_use,
)

# ---------------------------------------------------------------------------
# Shared fixtures
# ---------------------------------------------------------------------------

_ALL_PERMIT: dict[SourceUse, RightsDecision] = {use: RightsDecision.PERMIT for use in SourceUse}


def _make_activity(
    *,
    ai_allowed: bool = True,
    regions: frozenset[str] | None = None,
    max_retention_days: int = 90,
) -> ProcessingActivity:
    return ProcessingActivity(
        activity_id="act-rag-tax-rules",
        purpose="RAG retrieval over authoritative tax-rule corpus",
        role=ProcessingRole.CONTROLLER,
        legal_basis=LegalBasis.LEGAL_OBLIGATION,
        data_subject_categories=frozenset({DataSubjectCategory.SUBSCRIBER}),
        permitted_regions=regions if regions is not None else frozenset({"eu-west-1"}),
        ai_allowed=ai_allowed,
        max_retention_days=max_retention_days,
    )


def _make_profile(
    *,
    source_id: str = "src/tax-rules.md",
    decisions: dict[SourceUse, RightsDecision] | None = None,
) -> RightsProfile:
    return RightsProfile(
        source_id=source_id,
        source_class=SourceClass.S0,
        decisions=decisions if decisions is not None else dict(_ALL_PERMIT),
        licence_spdx="LicenseRef-ZoikoTax-Crown",
    )


def _make_customer(
    *,
    ai_opted_out: bool = False,
    denied_uses: frozenset[SourceUse] | None = None,
    max_retention_days: int | None = None,
) -> CustomerPolicy:
    return CustomerPolicy(
        customer_id="cust-acme-001",
        ai_opted_out=ai_opted_out,
        denied_uses=denied_uses if denied_uses is not None else frozenset(),
        max_retention_days=max_retention_days,
    )


def _make_provider_policy(
    *,
    no_training: bool = True,
    retention_days: int = 0,
) -> ProviderRetentionPolicy:
    return ProviderRetentionPolicy(
        provider_id="gcp:vertex-eu",
        no_training=no_training,
        retention_days=retention_days,
    )


# ---------------------------------------------------------------------------
# Construction invariants — ProcessingActivity
# ---------------------------------------------------------------------------


class TestProcessingActivityConstruction:
    def test_empty_activity_id_raises(self) -> None:
        with pytest.raises(PrivacySourceRightsError, match="activity_id"):
            ProcessingActivity(
                activity_id="",
                purpose="some purpose",
                role=ProcessingRole.CONTROLLER,
                legal_basis=LegalBasis.CONTRACT,
                data_subject_categories=frozenset({DataSubjectCategory.SUBSCRIBER}),
                permitted_regions=frozenset({"eu-west-1"}),
                ai_allowed=True,
                max_retention_days=30,
            )

    def test_empty_purpose_raises(self) -> None:
        with pytest.raises(PrivacySourceRightsError, match="purpose"):
            ProcessingActivity(
                activity_id="act-001",
                purpose="",
                role=ProcessingRole.CONTROLLER,
                legal_basis=LegalBasis.CONTRACT,
                data_subject_categories=frozenset({DataSubjectCategory.SUBSCRIBER}),
                permitted_regions=frozenset({"eu-west-1"}),
                ai_allowed=True,
                max_retention_days=30,
            )

    def test_empty_data_subject_categories_raises(self) -> None:
        with pytest.raises(PrivacySourceRightsError, match="data subject category"):
            ProcessingActivity(
                activity_id="act-001",
                purpose="valid purpose",
                role=ProcessingRole.CONTROLLER,
                legal_basis=LegalBasis.CONTRACT,
                data_subject_categories=frozenset(),
                permitted_regions=frozenset({"eu-west-1"}),
                ai_allowed=True,
                max_retention_days=30,
            )

    def test_negative_retention_days_raises(self) -> None:
        with pytest.raises(PrivacySourceRightsError, match="max_retention_days"):
            ProcessingActivity(
                activity_id="act-001",
                purpose="valid",
                role=ProcessingRole.CONTROLLER,
                legal_basis=LegalBasis.CONTRACT,
                data_subject_categories=frozenset({DataSubjectCategory.SUBSCRIBER}),
                permitted_regions=frozenset({"eu-west-1"}),
                ai_allowed=True,
                max_retention_days=-1,
            )

    def test_valid_construction(self) -> None:
        act = _make_activity()
        assert act.activity_id == "act-rag-tax-rules"
        assert act.ai_allowed is True
        assert act.permits_region("eu-west-1") is True
        assert act.permits_region("us-east-1") is False

    def test_zero_retention_days_allowed(self) -> None:
        """Zero means transient-only — explicitly valid."""
        act = _make_activity(max_retention_days=0)
        assert act.max_retention_days == 0


# ---------------------------------------------------------------------------
# Construction invariants — RightsProfile
# ---------------------------------------------------------------------------


class TestRightsProfileConstruction:
    def test_empty_source_id_raises(self) -> None:
        with pytest.raises(PrivacySourceRightsError, match="source_id"):
            RightsProfile(
                source_id="",
                source_class=SourceClass.S0,
                decisions=dict(_ALL_PERMIT),
                licence_spdx="LicenseRef-x",
            )

    def test_empty_licence_spdx_raises(self) -> None:
        with pytest.raises(PrivacySourceRightsError, match="licence_spdx"):
            RightsProfile(
                source_id="src/x.md",
                source_class=SourceClass.S3,
                decisions=dict(_ALL_PERMIT),
                licence_spdx="",
            )

    def test_missing_use_raises(self) -> None:
        """All five SourceUse values must be present."""
        partial = {SourceUse.USE_RETRIEVAL: RightsDecision.PERMIT}
        with pytest.raises(PrivacySourceRightsError, match="missing decisions"):
            RightsProfile(
                source_id="src/x.md",
                source_class=SourceClass.S3,
                decisions=partial,
                licence_spdx="Apache-2.0",
            )

    def test_all_permit(self) -> None:
        p = _make_profile()
        for use in SourceUse:
            assert p.permits(use) is True

    def test_unknown_collapses_to_deny(self) -> None:
        decisions = dict(_ALL_PERMIT)
        decisions[SourceUse.USE_TRAINING] = RightsDecision.UNKNOWN
        p = _make_profile(decisions=decisions)
        assert p.permits(SourceUse.USE_TRAINING) is False

    def test_deny_is_deny(self) -> None:
        decisions = dict(_ALL_PERMIT)
        decisions[SourceUse.USE_DISCLOSURE] = RightsDecision.DENY
        p = _make_profile(decisions=decisions)
        assert p.permits(SourceUse.USE_DISCLOSURE) is False


# ---------------------------------------------------------------------------
# Construction invariants — CustomerPolicy
# ---------------------------------------------------------------------------


class TestCustomerPolicyConstruction:
    def test_empty_customer_id_raises(self) -> None:
        with pytest.raises(PrivacySourceRightsError, match="customer_id"):
            CustomerPolicy(customer_id="")

    def test_negative_max_retention_days_raises(self) -> None:
        with pytest.raises(PrivacySourceRightsError, match="max_retention_days"):
            CustomerPolicy(customer_id="cust-001", max_retention_days=-1)

    def test_defaults(self) -> None:
        cp = CustomerPolicy(customer_id="cust-001")
        assert cp.ai_opted_out is False
        assert cp.denied_uses == frozenset()
        assert cp.max_retention_days is None

    def test_effective_permits_ceiling_deny(self) -> None:
        """Customer policy overrides a platform PERMIT to DENY."""
        profile = _make_profile()
        cp = CustomerPolicy(
            customer_id="c",
            denied_uses=frozenset({SourceUse.USE_TRAINING}),
        )
        assert cp.effective_permits(profile, SourceUse.USE_TRAINING) is False
        # Other uses still PERMIT
        assert cp.effective_permits(profile, SourceUse.USE_RETRIEVAL) is True

    def test_customer_cannot_expand_platform_deny(self) -> None:
        """Customer policy can't grant rights the platform denied."""
        decisions = dict(_ALL_PERMIT)
        decisions[SourceUse.USE_TRAINING] = RightsDecision.DENY
        profile = _make_profile(decisions=decisions)
        # Customer doesn't deny it — but platform already did
        cp = CustomerPolicy(customer_id="c")
        assert cp.effective_permits(profile, SourceUse.USE_TRAINING) is False

    def test_effective_max_retention_no_customer_limit(self) -> None:
        cp = CustomerPolicy(customer_id="c")
        assert cp.effective_max_retention_days(90) == 90

    def test_effective_max_retention_customer_is_tighter(self) -> None:
        cp = CustomerPolicy(customer_id="c", max_retention_days=30)
        assert cp.effective_max_retention_days(90) == 30

    def test_effective_max_retention_platform_is_tighter(self) -> None:
        cp = CustomerPolicy(customer_id="c", max_retention_days=90)
        assert cp.effective_max_retention_days(7) == 7


# ---------------------------------------------------------------------------
# Construction invariants — ProviderRetentionPolicy
# ---------------------------------------------------------------------------


class TestProviderRetentionPolicyConstruction:
    def test_empty_provider_id_raises(self) -> None:
        with pytest.raises(PrivacySourceRightsError, match="provider_id"):
            ProviderRetentionPolicy(provider_id="", no_training=True, retention_days=0)

    def test_negative_retention_days_raises(self) -> None:
        with pytest.raises(PrivacySourceRightsError, match="retention_days"):
            ProviderRetentionPolicy(
                provider_id="p", no_training=True, retention_days=-1
            )

    def test_valid(self) -> None:
        pp = _make_provider_policy()
        assert pp.no_training is True
        assert pp.retention_days == 0


# ---------------------------------------------------------------------------
# PrivacySourceRightsError attributes
# ---------------------------------------------------------------------------


class TestPrivacySourceRightsError:
    def test_carries_refusal_and_detail(self) -> None:
        err = PrivacySourceRightsError(
            "test detail", PrivacyRefusal.CUSTOMER_OPT_OUT
        )
        assert err.refusal is PrivacyRefusal.CUSTOMER_OPT_OUT
        assert err.detail == "test detail"
        assert str(err).startswith("PRIVACY_CUSTOMER_OPT_OUT:")

    def test_no_refusal(self) -> None:
        err = PrivacySourceRightsError("raw error")
        assert err.refusal is None
        assert "PRIVACY_SOURCE_RIGHTS_ERROR" in str(err)


# ---------------------------------------------------------------------------
# §18 Rule 1: check_invocation_privacy
# ---------------------------------------------------------------------------


class TestCheckInvocationPrivacy:
    def test_happy_path(self) -> None:
        """Valid activity + region → no exception."""
        check_invocation_privacy(_make_activity(), "eu-west-1")

    def test_happy_path_with_customer_policy(self) -> None:
        check_invocation_privacy(
            _make_activity(), "eu-west-1", customer_policy=_make_customer()
        )

    def test_customer_opt_out_blocks_before_activity_flag(self) -> None:
        """Customer opt-out is checked first — even if activity allows AI."""
        cp = _make_customer(ai_opted_out=True)
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_invocation_privacy(_make_activity(), "eu-west-1", customer_policy=cp)
        assert exc_info.value.refusal is PrivacyRefusal.CUSTOMER_OPT_OUT

    def test_activity_ai_blocked(self) -> None:
        activity = _make_activity(ai_allowed=False)
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_invocation_privacy(activity, "eu-west-1")
        assert exc_info.value.refusal is PrivacyRefusal.ACTIVITY_AI_BLOCKED

    def test_region_denied(self) -> None:
        activity = _make_activity(regions=frozenset({"eu-central-1"}))
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_invocation_privacy(activity, "us-east-1")
        assert exc_info.value.refusal is PrivacyRefusal.ACTIVITY_REGION_DENIED

    def test_customer_opt_out_takes_precedence_over_activity_ai_blocked(self) -> None:
        """Opt-out is a wider blast radius than activity block — checked first."""
        activity = _make_activity(ai_allowed=False)
        cp = _make_customer(ai_opted_out=True)
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_invocation_privacy(activity, "eu-west-1", customer_policy=cp)
        # Opt-out code appears, not activity-blocked code
        assert exc_info.value.refusal is PrivacyRefusal.CUSTOMER_OPT_OUT

    def test_region_not_permitted_empty_regions(self) -> None:
        """Empty permitted_regions means no region is allowed — fail-safe."""
        activity = _make_activity(regions=frozenset())
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_invocation_privacy(activity, "eu-west-1")
        assert exc_info.value.refusal is PrivacyRefusal.ACTIVITY_REGION_DENIED


# ---------------------------------------------------------------------------
# §18 Rule 2: check_source_use — platform rights
# ---------------------------------------------------------------------------


class TestCheckSourceUsePlatform:
    def test_all_uses_permitted(self) -> None:
        profile = _make_profile()
        for use in SourceUse:
            record = check_source_use(profile, use)
            assert isinstance(record, SourceRightsRecord)
            assert record.decision is RightsDecision.PERMIT
            assert record.use is use
            assert record.source_id == profile.source_id
            assert record.customer_id == ""

    def test_deny_raises_source_rights_denied(self) -> None:
        decisions = dict(_ALL_PERMIT)
        decisions[SourceUse.USE_TRAINING] = RightsDecision.DENY
        profile = _make_profile(decisions=decisions)
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_source_use(profile, SourceUse.USE_TRAINING)
        assert exc_info.value.refusal is PrivacyRefusal.SOURCE_RIGHTS_DENIED

    def test_unknown_collapses_to_deny(self) -> None:
        """UNKNOWN → DENY (ZTAX-SRC-REQ-0005)."""
        decisions = dict(_ALL_PERMIT)
        decisions[SourceUse.USE_EMBEDDING] = RightsDecision.UNKNOWN
        profile = _make_profile(decisions=decisions)
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_source_use(profile, SourceUse.USE_EMBEDDING)
        assert exc_info.value.refusal is PrivacyRefusal.SOURCE_RIGHTS_DENIED
        assert "UNKNOWN" in exc_info.value.detail

    def test_uses_are_independent(self) -> None:
        """Permit for retrieval does not imply permit for training."""
        decisions = {
            SourceUse.USE_RETRIEVAL: RightsDecision.PERMIT,
            SourceUse.USE_EMBEDDING: RightsDecision.PERMIT,
            SourceUse.USE_DISCLOSURE: RightsDecision.DENY,
            SourceUse.USE_EVALUATION: RightsDecision.PERMIT,
            SourceUse.USE_TRAINING: RightsDecision.DENY,
        }
        profile = _make_profile(decisions=decisions)
        # Permitted uses succeed
        check_source_use(profile, SourceUse.USE_RETRIEVAL)
        check_source_use(profile, SourceUse.USE_EMBEDDING)
        # Denied uses raise
        with pytest.raises(PrivacySourceRightsError):
            check_source_use(profile, SourceUse.USE_DISCLOSURE)
        with pytest.raises(PrivacySourceRightsError):
            check_source_use(profile, SourceUse.USE_TRAINING)


# ---------------------------------------------------------------------------
# §18 Rule 3: customer policy ceiling
# ---------------------------------------------------------------------------


class TestCustomerPolicyCeiling:
    def test_customer_denies_additional_use(self) -> None:
        """Customer adds a DENY on top of platform PERMIT."""
        profile = _make_profile()
        cp = _make_customer(denied_uses=frozenset({SourceUse.USE_TRAINING}))
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_source_use(profile, SourceUse.USE_TRAINING, customer_policy=cp)
        assert exc_info.value.refusal is PrivacyRefusal.CUSTOMER_POLICY_DENIED

    def test_customer_cannot_override_platform_deny(self) -> None:
        """Customer policy cannot turn a DENY into a PERMIT."""
        decisions = dict(_ALL_PERMIT)
        decisions[SourceUse.USE_EVALUATION] = RightsDecision.DENY
        profile = _make_profile(decisions=decisions)
        # Customer has empty denied_uses — does NOT re-permit evaluation
        cp = _make_customer()
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_source_use(profile, SourceUse.USE_EVALUATION, customer_policy=cp)
        # Platform DENY fires first — SOURCE_RIGHTS_DENIED, not CUSTOMER_POLICY_DENIED
        assert exc_info.value.refusal is PrivacyRefusal.SOURCE_RIGHTS_DENIED

    def test_customer_record_carries_customer_id(self) -> None:
        profile = _make_profile()
        cp = _make_customer()
        record = check_source_use(
            profile, SourceUse.USE_RETRIEVAL, customer_policy=cp
        )
        assert record.customer_id == "cust-acme-001"
        assert record.decision is RightsDecision.PERMIT

    def test_customer_denial_carries_customer_id_in_error(self) -> None:
        profile = _make_profile()
        cp = _make_customer(denied_uses=frozenset({SourceUse.USE_DISCLOSURE}))
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_source_use(profile, SourceUse.USE_DISCLOSURE, customer_policy=cp)
        assert "cust-acme-001" in exc_info.value.detail


# ---------------------------------------------------------------------------
# §18 Rule 4: corpus build guard
# ---------------------------------------------------------------------------


class TestCheckCorpusSource:
    def test_both_retrieval_and_embedding_required(self) -> None:
        profile = _make_profile()
        ret_rec, emb_rec = check_corpus_source(profile)
        assert ret_rec.use is SourceUse.USE_RETRIEVAL
        assert emb_rec.use is SourceUse.USE_EMBEDDING
        assert ret_rec.decision is RightsDecision.PERMIT
        assert emb_rec.decision is RightsDecision.PERMIT

    def test_retrieval_denied_blocks_corpus(self) -> None:
        decisions = dict(_ALL_PERMIT)
        decisions[SourceUse.USE_RETRIEVAL] = RightsDecision.DENY
        profile = _make_profile(decisions=decisions)
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_corpus_source(profile)
        assert exc_info.value.refusal is PrivacyRefusal.SOURCE_RIGHTS_DENIED

    def test_embedding_denied_blocks_corpus(self) -> None:
        decisions = dict(_ALL_PERMIT)
        decisions[SourceUse.USE_EMBEDDING] = RightsDecision.DENY
        profile = _make_profile(decisions=decisions)
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_corpus_source(profile)
        assert exc_info.value.refusal is PrivacyRefusal.SOURCE_RIGHTS_DENIED

    def test_embedding_unknown_blocks_corpus(self) -> None:
        """UNKNOWN is treated as DENY for corpus build (ZTAX-SRC-REQ-0005)."""
        decisions = dict(_ALL_PERMIT)
        decisions[SourceUse.USE_EMBEDDING] = RightsDecision.UNKNOWN
        profile = _make_profile(decisions=decisions)
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_corpus_source(profile)
        assert exc_info.value.refusal is PrivacyRefusal.SOURCE_RIGHTS_DENIED

    def test_customer_additional_denial_blocks_corpus(self) -> None:
        profile = _make_profile()
        cp = _make_customer(denied_uses=frozenset({SourceUse.USE_EMBEDDING}))
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_corpus_source(profile, customer_policy=cp)
        assert exc_info.value.refusal is PrivacyRefusal.CUSTOMER_POLICY_DENIED

    def test_other_uses_not_required(self) -> None:
        """Corpus build only needs retrieval+embedding; training may be denied."""
        decisions = dict(_ALL_PERMIT)
        decisions[SourceUse.USE_TRAINING] = RightsDecision.DENY
        decisions[SourceUse.USE_DISCLOSURE] = RightsDecision.DENY
        profile = _make_profile(decisions=decisions)
        ret_rec, emb_rec = check_corpus_source(profile)
        assert ret_rec.decision is RightsDecision.PERMIT
        assert emb_rec.decision is RightsDecision.PERMIT


# ---------------------------------------------------------------------------
# §18 Rule 5: provider retention / no-training checks
# ---------------------------------------------------------------------------


class TestCheckProviderRetention:
    def test_happy_path_non_personal(self) -> None:
        """Non-personal data class (P0, INTERNAL) — no_training not required."""
        pp = _make_provider_policy(no_training=False, retention_days=30)
        # Should not raise for non-personal class
        check_provider_retention(pp, _make_activity(max_retention_days=90), "P0")

    def test_happy_path_personal_no_training_true(self) -> None:
        pp = _make_provider_policy(no_training=True, retention_days=0)
        check_provider_retention(pp, _make_activity(), "P3")

    def test_personal_data_no_training_false_raises(self) -> None:
        """P2+ data class + no_training=False → PROVIDER_TRAINS_ON_PERSONAL_DATA."""
        pp = _make_provider_policy(no_training=False, retention_days=0)
        for data_class in ("P2", "P3", "P4", "P5", "P6"):
            with pytest.raises(PrivacySourceRightsError) as exc_info:
                check_provider_retention(pp, _make_activity(), data_class)
            assert exc_info.value.refusal is PrivacyRefusal.PROVIDER_TRAINS_ON_PERSONAL_DATA

    def test_p0_and_p1_are_not_personal(self) -> None:
        """P0 and P1 are not treated as personal — no_training not enforced."""
        pp = _make_provider_policy(no_training=False, retention_days=0)
        # Must not raise
        check_provider_retention(pp, _make_activity(), "P0")
        check_provider_retention(pp, _make_activity(), "P1")
        check_provider_retention(pp, _make_activity(), "INTERNAL")

    def test_retention_exceeded_raises(self) -> None:
        """Provider retention > activity maximum → PROVIDER_RETENTION_EXCEEDED."""
        pp = _make_provider_policy(no_training=True, retention_days=91)
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_provider_retention(pp, _make_activity(max_retention_days=90), "P0")
        assert exc_info.value.refusal is PrivacyRefusal.PROVIDER_RETENTION_EXCEEDED

    def test_retention_at_maximum_is_ok(self) -> None:
        pp = _make_provider_policy(no_training=True, retention_days=90)
        check_provider_retention(pp, _make_activity(max_retention_days=90), "P0")

    def test_retention_exceeded_with_customer_ceiling(self) -> None:
        """Customer imposes a tighter ceiling than the activity."""
        pp = _make_provider_policy(no_training=True, retention_days=60)
        activity = _make_activity(max_retention_days=90)
        cp = _make_customer(max_retention_days=30)
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_provider_retention(pp, activity, "P0", customer_policy=cp)
        assert exc_info.value.refusal is PrivacyRefusal.PROVIDER_RETENTION_EXCEEDED

    def test_retention_ok_within_customer_ceiling(self) -> None:
        pp = _make_provider_policy(no_training=True, retention_days=30)
        activity = _make_activity(max_retention_days=90)
        cp = _make_customer(max_retention_days=60)
        # 30 <= min(60, 90) → OK
        check_provider_retention(pp, activity, "P0", customer_policy=cp)

    def test_transient_provider_always_passes_retention(self) -> None:
        """retention_days=0 always passes regardless of ceiling."""
        pp = _make_provider_policy(no_training=True, retention_days=0)
        check_provider_retention(pp, _make_activity(max_retention_days=0), "P0")

    def test_no_training_checked_before_retention(self) -> None:
        """no_training gate fires first for P2+ — same blast-radius discipline."""
        pp = _make_provider_policy(no_training=False, retention_days=91)
        activity = _make_activity(max_retention_days=90)
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_provider_retention(pp, activity, "P3")
        assert exc_info.value.refusal is PrivacyRefusal.PROVIDER_TRAINS_ON_PERSONAL_DATA


# ---------------------------------------------------------------------------
# §18 Rule 6: customer opt-out routing
# ---------------------------------------------------------------------------


class TestCustomerOptOut:
    def test_opted_out_raises_customer_opt_out(self) -> None:
        cp = _make_customer(ai_opted_out=True)
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_invocation_privacy(_make_activity(), "eu-west-1", customer_policy=cp)
        assert exc_info.value.refusal is PrivacyRefusal.CUSTOMER_OPT_OUT
        # Error message tells caller to route to deterministic/manual path
        assert "deterministic" in exc_info.value.detail.lower()

    def test_not_opted_out_proceeds(self) -> None:
        cp = _make_customer(ai_opted_out=False)
        check_invocation_privacy(_make_activity(), "eu-west-1", customer_policy=cp)

    def test_opt_out_error_carries_customer_id(self) -> None:
        cp = _make_customer(ai_opted_out=True)
        with pytest.raises(PrivacySourceRightsError) as exc_info:
            check_invocation_privacy(_make_activity(), "eu-west-1", customer_policy=cp)
        assert "cust-acme-001" in exc_info.value.detail


# ---------------------------------------------------------------------------
# SourceRightsRecord structure
# ---------------------------------------------------------------------------


class TestSourceRightsRecord:
    def test_record_fields(self) -> None:
        profile = _make_profile()
        record = check_source_use(profile, SourceUse.USE_RETRIEVAL)
        assert record.source_id == "src/tax-rules.md"
        assert record.use is SourceUse.USE_RETRIEVAL
        assert record.decision is RightsDecision.PERMIT
        assert record.customer_id == ""
        assert record.rationale  # Non-empty

    def test_record_is_frozen(self) -> None:
        profile = _make_profile()
        record = check_source_use(profile, SourceUse.USE_RETRIEVAL)
        with pytest.raises((AttributeError, TypeError)):
            record.decision = RightsDecision.DENY  # type: ignore[misc]


# ---------------------------------------------------------------------------
# Cross-cutting: all PrivacyRefusal codes are reachable
# ---------------------------------------------------------------------------


class TestAllRefusalCodesReachable:
    """Ensure every PrivacyRefusal code maps to a real execution path."""

    @pytest.mark.parametrize(
        "refusal",
        list(PrivacyRefusal),
    )
    def test_refusal_code_exists(self, refusal: PrivacyRefusal) -> None:
        assert isinstance(refusal.value, str)
        assert refusal.value.startswith("PRIVACY_")
