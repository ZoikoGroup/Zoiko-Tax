"""Tests for the Human Review Queue and Decision Record.

Chapter 17 §20 and §25 of the ZoikoTax Master Specification.

Mirrors the structure and discipline of ``test_evaluation.py`` and
``test_tool_broker.py``:

- Plain functions; no test classes.
- ``dataclasses.replace`` for variants so a typo in a field name is a
  type error here, not a silent default.
- Every public type and every public method is exercised.
- Every error path is explicitly asserted so a regression surfaces as a
  test failure, not as a silent wrong answer in an audit log.

Test groups
-----------
  ReviewerProfile         -- construction, immutability, validation, as_dict
  ReviewItem              -- construction, immutability, deterministic ID
  ReviewDecision          -- construction, immutability, as_dict
  ReviewQueue.submit      -- happy path, governance gate, duplicate guard
  ReviewQueue.decide      -- APPROVE / REJECT / ESCALATE / MODIFY transitions,
                            terminal-state guard, qualification checks,
                            correction enforcement
  ReviewQueue.pop         -- FIFO, empty queue, respects non-PENDING items
  ReviewQueue.get / status / decisions
  ReviewQueue counts      -- pending_count, escalated_count
  TTL / EXPIRED           -- item past deadline transitions
  Full audit trail        -- integration tests
"""

from __future__ import annotations

import random
import time
from dataclasses import FrozenInstanceError, replace
from datetime import UTC, datetime

import pytest

from ztax_gateway.citation import Citation, SourceChunk, combine
from ztax_gateway.governance import GovernanceRefusedError, UseCase, UseCaseRegistry
from ztax_gateway.human_review import (
    DecisionReasonCode,
    EvidencePanel,
    PromotedRecord,
    ReviewDecision,
    ReviewerProfile,
    ReviewError,
    ReviewItem,
    ReviewOutcome,
    ReviewPriority,
    ReviewQueue,
    ReviewStatus,
)
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier

# ---------------------------------------------------------------------------
# Shared fixtures
# ---------------------------------------------------------------------------

BASE_USE_CASE = UseCase(
    use_case_id="human-review",
    owner="lane-l",
    description="Submit AI outputs for human review",
    max_risk_tier=RiskTier.T2,
    max_authority=AuthorityOutcome.A2,
    permitted_regions=frozenset({"eu-west-1"}),
)

BASE_PROVENANCE = Provenance(
    use_case="human-review",
    model_profile="model:reviewer@2026.09",
    provider_profile="provider:eu-hosted",
    prompt_profile="prompt:review@1",
    region="eu-west-1",
    data_class="INTERNAL",
    risk_tier=RiskTier.T1,
    authority_outcome=AuthorityOutcome.A1,
    ai_train_version="0.5.0",
)

BASE_REVIEWER_PROFILE = ReviewerProfile(
    reviewer_id="reviewer:alice",
    max_authority=AuthorityOutcome.A2,
    permitted_regions=frozenset({"eu-west-1"}),
    permitted_jurisdictions=frozenset({"EU-VAT", "EU-CUSTOMS"}),
    qualified_since=datetime(2026, 1, 1, tzinfo=UTC),
)


def _registry(*extra: UseCase) -> UseCaseRegistry:
    return UseCaseRegistry([BASE_USE_CASE, *extra])


def _citation(
    source_id: str = "spec-doc-a", content: bytes = b"tax rule section 12"
) -> Citation:
    chunk = SourceChunk(
        source_id=source_id,
        content=content,
        byte_start=0,
        byte_end=len(content),
    )
    return combine([chunk])


def _queue() -> ReviewQueue:
    return ReviewQueue()


def _submit(
    q: ReviewQueue,
    output_id: str = "eval-report-001",
    summary: str = "SKU classification proposal requires review",
    citation: Citation | None = None,
) -> ReviewItem:
    return q.submit(
        output_id=output_id,
        citation=citation or _citation(),
        summary=summary,
        provenance=BASE_PROVENANCE,
        registry=_registry(),
    )


def _decide(
    q: ReviewQueue,
    item: ReviewItem,
    outcome: ReviewOutcome = ReviewOutcome.APPROVE,
    reviewer_profile: ReviewerProfile | None = None,
    rationale: str = "",
    correction: str = "",
) -> ReviewDecision:
    return q.decide(
        review_item_id=item.review_item_id,
        reviewer_profile=reviewer_profile or BASE_REVIEWER_PROFILE,
        outcome=outcome,
        rationale=rationale,
        correction=correction,
    )


# ---------------------------------------------------------------------------
# ReviewerProfile -- construction
# ---------------------------------------------------------------------------


def test_reviewer_profile_stores_reviewer_id() -> None:
    assert BASE_REVIEWER_PROFILE.reviewer_id == "reviewer:alice"


def test_reviewer_profile_stores_max_authority() -> None:
    assert BASE_REVIEWER_PROFILE.max_authority is AuthorityOutcome.A2


def test_reviewer_profile_stores_permitted_regions() -> None:
    assert "eu-west-1" in BASE_REVIEWER_PROFILE.permitted_regions


def test_reviewer_profile_stores_permitted_jurisdictions() -> None:
    assert "EU-VAT" in BASE_REVIEWER_PROFILE.permitted_jurisdictions


def test_reviewer_profile_stores_qualified_since() -> None:
    assert BASE_REVIEWER_PROFILE.qualified_since == datetime(2026, 1, 1, tzinfo=UTC)


# ---------------------------------------------------------------------------
# ReviewerProfile -- validation
# ---------------------------------------------------------------------------


def test_reviewer_profile_empty_reviewer_id_is_refused() -> None:
    with pytest.raises(ReviewError, match="reviewer_id"):
        ReviewerProfile(
            reviewer_id="",
            max_authority=AuthorityOutcome.A1,
            permitted_regions=frozenset({"eu-west-1"}),
            permitted_jurisdictions=frozenset(),
            qualified_since=datetime.now(tz=UTC),
        )


def test_reviewer_profile_empty_regions_is_refused() -> None:
    with pytest.raises(ReviewError, match="permitted_regions"):
        ReviewerProfile(
            reviewer_id="reviewer:alice",
            max_authority=AuthorityOutcome.A1,
            permitted_regions=frozenset(),
            permitted_jurisdictions=frozenset(),
            qualified_since=datetime.now(tz=UTC),
        )


# ---------------------------------------------------------------------------
# ReviewerProfile -- immutability
# ---------------------------------------------------------------------------


def test_reviewer_profile_is_frozen() -> None:
    with pytest.raises((FrozenInstanceError, AttributeError)):
        BASE_REVIEWER_PROFILE.reviewer_id = "tampered"  # type: ignore[misc]


def test_reviewer_profile_max_authority_cannot_be_mutated() -> None:
    with pytest.raises((FrozenInstanceError, AttributeError)):
        BASE_REVIEWER_PROFILE.max_authority = AuthorityOutcome.A5  # type: ignore[misc]


# ---------------------------------------------------------------------------
# ReviewerProfile -- as_dict
# ---------------------------------------------------------------------------


def test_reviewer_profile_as_dict_contains_expected_keys() -> None:
    d = BASE_REVIEWER_PROFILE.as_dict()
    assert set(d) >= {
        "reviewer_id",
        "max_authority",
        "permitted_regions",
        "permitted_jurisdictions",
        "qualified_since",
    }


def test_reviewer_profile_as_dict_max_authority_is_string() -> None:
    assert BASE_REVIEWER_PROFILE.as_dict()["max_authority"] == "A2"


def test_reviewer_profile_as_dict_regions_are_sorted_list() -> None:
    prof = replace(
        BASE_REVIEWER_PROFILE,
        permitted_regions=frozenset({"eu-west-1", "eu-central-1"}),
    )
    assert prof.as_dict()["permitted_regions"] == ["eu-central-1", "eu-west-1"]


# ---------------------------------------------------------------------------
# ReviewItem -- construction
# ---------------------------------------------------------------------------


def test_review_item_has_pending_status_on_creation() -> None:
    item = _submit(_queue())
    assert item.status is ReviewStatus.PENDING


def test_review_item_has_submitted_at_timestamp() -> None:
    before = datetime.now(tz=UTC)
    item = _submit(_queue())
    after = datetime.now(tz=UTC)
    assert before <= item.submitted_at <= after


def test_review_item_stores_output_id() -> None:
    item = _submit(_queue(), output_id="my-output-99")
    assert item.output_id == "my-output-99"


def test_review_item_stores_summary() -> None:
    item = _submit(_queue(), summary="Rate change candidate for EU-VAT")
    assert item.summary == "Rate change candidate for EU-VAT"


def test_review_item_stores_citation() -> None:
    cit = _citation(content=b"specific regulation text")
    item = _submit(_queue(), citation=cit)
    assert item.citation.citation_id == cit.citation_id


def test_review_item_stores_provenance() -> None:
    item = _submit(_queue())
    assert item.provenance.use_case == "human-review"


# ---------------------------------------------------------------------------
# ReviewItem -- deterministic ID
# ---------------------------------------------------------------------------


def test_review_item_id_is_deterministic_for_same_output_and_citation() -> None:
    cit = _citation()
    q1 = ReviewQueue()
    q2 = ReviewQueue()
    item1 = _submit(q1, citation=cit)
    item2 = _submit(q2, citation=cit)
    assert item1.review_item_id == item2.review_item_id


def test_review_item_id_differs_for_different_output_ids() -> None:
    cit = _citation()
    q = ReviewQueue()
    item1 = _submit(q, output_id="out-A", citation=cit)
    item2 = _submit(q, output_id="out-B", citation=_citation(content=b"different"))
    assert item1.review_item_id != item2.review_item_id


def test_review_item_id_is_sixteen_hex_chars() -> None:
    item = _submit(_queue())
    assert len(item.review_item_id) == 16
    assert all(c in "0123456789abcdef" for c in item.review_item_id)


# ---------------------------------------------------------------------------
# ReviewItem -- immutability
# ---------------------------------------------------------------------------


def test_review_item_is_frozen() -> None:
    item = _submit(_queue())
    with pytest.raises((FrozenInstanceError, AttributeError)):
        item.output_id = "tampered"  # type: ignore[misc]


def test_review_item_status_field_cannot_be_mutated() -> None:
    item = _submit(_queue())
    with pytest.raises((FrozenInstanceError, AttributeError)):
        item.status = ReviewStatus.APPROVED  # type: ignore[misc]


def test_review_item_citation_field_cannot_be_mutated() -> None:
    item = _submit(_queue())
    with pytest.raises((FrozenInstanceError, AttributeError)):
        item.citation = _citation(content=b"tampered citation")  # type: ignore[misc]


# ---------------------------------------------------------------------------
# ReviewItem -- validation
# ---------------------------------------------------------------------------


def test_empty_output_id_is_refused() -> None:
    with pytest.raises(ReviewError, match="output_id"):
        ReviewItem(
            output_id="",
            citation=_citation(),
            summary="valid summary",
            provenance=BASE_PROVENANCE,
        )


def test_empty_summary_is_refused() -> None:
    with pytest.raises(ReviewError, match="summary"):
        ReviewItem(
            output_id="out-001",
            citation=_citation(),
            summary="",
            provenance=BASE_PROVENANCE,
        )


# ---------------------------------------------------------------------------
# ReviewItem -- as_dict
# ---------------------------------------------------------------------------


def test_review_item_as_dict_contains_expected_keys() -> None:
    item = _submit(_queue())
    d = item.as_dict()
    assert set(d) >= {
        "review_item_id",
        "output_id",
        "citation_id",
        "summary",
        "status",
        "submitted_at",
        "use_case",
        "region",
        "risk_tier",
        "authority_outcome",
    }


def test_review_item_as_dict_status_is_pending_string() -> None:
    item = _submit(_queue())
    assert item.as_dict()["status"] == "PENDING"


# ---------------------------------------------------------------------------
# ReviewDecision -- construction
# ---------------------------------------------------------------------------


def _make_decision(
    review_item_id: str = "abcdef1234567890",
    output_id: str = "out-001",
    reviewer_profile: ReviewerProfile | None = None,
    outcome: ReviewOutcome = ReviewOutcome.APPROVE,
    rationale: str = "",
    correction: str = "",
) -> ReviewDecision:
    return ReviewDecision(
        review_item_id=review_item_id,
        output_id=output_id,
        reviewer_profile=reviewer_profile or BASE_REVIEWER_PROFILE,
        outcome=outcome,
        decided_at=datetime.now(tz=UTC),
        decision_id="decision01234567",
        rationale=rationale,
        correction=correction,
    )


def test_review_decision_stores_reviewer_id_from_profile() -> None:
    d = _make_decision()
    assert d.reviewer_id == "reviewer:alice"


def test_review_decision_reviewer_id_matches_profile() -> None:
    prof = replace(BASE_REVIEWER_PROFILE, reviewer_id="reviewer:bob")
    d = _make_decision(reviewer_profile=prof)
    assert d.reviewer_id == "reviewer:bob"
    assert d.reviewer_profile.reviewer_id == "reviewer:bob"


def test_review_decision_stores_outcome() -> None:
    d = _make_decision(outcome=ReviewOutcome.REJECT)
    assert d.outcome is ReviewOutcome.REJECT


def test_review_decision_stores_rationale() -> None:
    d = _make_decision(rationale="Insufficient citation coverage")
    assert d.rationale == "Insufficient citation coverage"


def test_review_decision_stores_profile_snapshot() -> None:
    d = _make_decision()
    assert d.reviewer_profile.max_authority is AuthorityOutcome.A2


# ---------------------------------------------------------------------------
# ReviewDecision -- MODIFY / correction invariants
# ---------------------------------------------------------------------------


def test_modify_decision_requires_correction() -> None:
    with pytest.raises(ReviewError, match="requires a non-empty correction"):
        _make_decision(outcome=ReviewOutcome.MODIFY, correction="")


def test_modify_decision_with_correction_is_accepted() -> None:
    d = _make_decision(
        outcome=ReviewOutcome.MODIFY,
        correction="Corrected: EU-VAT exemption article 135(1)(a) applies",
    )
    assert d.outcome is ReviewOutcome.MODIFY
    assert d.correction == "Corrected: EU-VAT exemption article 135(1)(a) applies"


def test_approve_decision_with_correction_is_refused() -> None:
    with pytest.raises(ReviewError, match="must not carry a correction"):
        _make_decision(outcome=ReviewOutcome.APPROVE, correction="should not be here")


def test_reject_decision_with_correction_is_refused() -> None:
    with pytest.raises(ReviewError, match="must not carry a correction"):
        _make_decision(outcome=ReviewOutcome.REJECT, correction="should not be here")


def test_escalate_decision_with_correction_is_refused() -> None:
    with pytest.raises(ReviewError, match="must not carry a correction"):
        _make_decision(outcome=ReviewOutcome.ESCALATE, correction="should not be here")


# ---------------------------------------------------------------------------
# ReviewDecision -- immutability
# ---------------------------------------------------------------------------


def test_review_decision_is_frozen() -> None:
    d = _make_decision()
    with pytest.raises((FrozenInstanceError, AttributeError)):
        d.reviewer_id = "tampered"  # type: ignore[misc]


def test_review_decision_outcome_cannot_be_mutated() -> None:
    d = _make_decision()
    with pytest.raises((FrozenInstanceError, AttributeError)):
        d.outcome = ReviewOutcome.REJECT  # type: ignore[misc]


def test_review_decision_reviewer_profile_cannot_be_mutated() -> None:
    d = _make_decision()
    with pytest.raises((FrozenInstanceError, AttributeError)):
        d.reviewer_profile = BASE_REVIEWER_PROFILE  # type: ignore[misc]


# ---------------------------------------------------------------------------
# ReviewDecision -- as_dict
# ---------------------------------------------------------------------------


def test_review_decision_as_dict_contains_expected_keys() -> None:
    d = _make_decision()
    keys = set(d.as_dict())
    expected = {
        "decision_id", "review_item_id", "output_id",
        "reviewer_id", "reviewer_profile", "outcome", "decided_at",
    }
    assert expected <= keys


def test_review_decision_as_dict_reviewer_profile_is_nested_dict() -> None:
    d = _make_decision()
    profile_dict = d.as_dict()["reviewer_profile"]
    assert isinstance(profile_dict, dict)
    assert profile_dict["reviewer_id"] == "reviewer:alice"


def test_review_decision_as_dict_omits_rationale_when_empty() -> None:
    d = _make_decision(rationale="")
    assert "rationale" not in d.as_dict()


def test_review_decision_as_dict_includes_rationale_when_set() -> None:
    d = _make_decision(rationale="approved -- cit hash verified")
    assert d.as_dict()["rationale"] == "approved -- cit hash verified"


def test_review_decision_as_dict_omits_correction_when_empty() -> None:
    d = _make_decision(outcome=ReviewOutcome.APPROVE)
    assert "correction" not in d.as_dict()


def test_review_decision_as_dict_includes_correction_for_modify() -> None:
    d = _make_decision(
        outcome=ReviewOutcome.MODIFY,
        correction="Fixed: data service, not voice",
    )
    assert d.as_dict()["correction"] == "Fixed: data service, not voice"


def test_review_decision_as_dict_outcome_is_string() -> None:
    d = _make_decision(outcome=ReviewOutcome.ESCALATE)
    assert d.as_dict()["outcome"] == "ESCALATE"


# ---------------------------------------------------------------------------
# ReviewQueue.submit -- happy path
# ---------------------------------------------------------------------------


def test_submit_returns_review_item() -> None:
    item = _submit(_queue())
    assert isinstance(item, ReviewItem)


def test_submit_item_appears_in_queue() -> None:
    q = _queue()
    item = _submit(q)
    assert q.get(item.review_item_id) is item


def test_submit_increments_pending_count() -> None:
    q = _queue()
    assert q.pending_count() == 0
    _submit(q, output_id="out-1", citation=_citation(content=b"c1"))
    assert q.pending_count() == 1
    _submit(q, output_id="out-2", citation=_citation(content=b"c2"))
    assert q.pending_count() == 2


# ---------------------------------------------------------------------------
# ReviewQueue.submit -- governance gate
# ---------------------------------------------------------------------------


def test_submit_with_unknown_use_case_is_refused() -> None:
    q = _queue()
    unknown_prov = replace(BASE_PROVENANCE, use_case="nonexistent-use-case")
    with pytest.raises(GovernanceRefusedError):
        q.submit(
            output_id="out-001",
            citation=_citation(),
            summary="test",
            provenance=unknown_prov,
            registry=_registry(),
        )


def test_submit_with_killed_use_case_is_refused() -> None:
    q = _queue()
    reg = _registry()
    reg.kill("human-review")
    with pytest.raises(GovernanceRefusedError):
        q.submit(
            output_id="out-001",
            citation=_citation(),
            summary="test",
            provenance=BASE_PROVENANCE,
            registry=reg,
        )


def test_submit_with_global_kill_engaged_is_refused() -> None:
    q = _queue()
    reg = _registry()
    reg.engage_global_kill()
    with pytest.raises(GovernanceRefusedError):
        q.submit(
            output_id="out-001",
            citation=_citation(),
            summary="test",
            provenance=BASE_PROVENANCE,
            registry=reg,
        )


def test_submit_with_region_not_permitted_is_refused() -> None:
    q = _queue()
    wrong_region = replace(BASE_PROVENANCE, region="us-east-1")
    with pytest.raises(GovernanceRefusedError):
        q.submit(
            output_id="out-001",
            citation=_citation(),
            summary="test",
            provenance=wrong_region,
            registry=_registry(),
        )


# ---------------------------------------------------------------------------
# ReviewQueue.submit -- duplicate guard
# ---------------------------------------------------------------------------


def test_submitting_same_output_and_citation_twice_raises() -> None:
    q = _queue()
    cit = _citation()
    _submit(q, output_id="out-dup", citation=cit)
    with pytest.raises(ReviewError, match="already in the queue"):
        _submit(q, output_id="out-dup", citation=cit)


def test_different_citation_same_output_id_does_not_collide() -> None:
    q = _queue()
    _submit(q, output_id="out-x", citation=_citation(content=b"version-1"))
    item2 = _submit(q, output_id="out-x", citation=_citation(content=b"version-2"))
    assert item2.output_id == "out-x"


# ---------------------------------------------------------------------------
# ReviewQueue.decide -- APPROVE
# ---------------------------------------------------------------------------


def test_approve_transitions_item_to_approved() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.APPROVE)
    assert q.status(item.review_item_id) is ReviewStatus.APPROVED


def test_approve_returns_review_decision() -> None:
    q = _queue()
    item = _submit(q)
    decision = _decide(q, item, ReviewOutcome.APPROVE)
    assert isinstance(decision, ReviewDecision)
    assert decision.outcome is ReviewOutcome.APPROVE
    assert decision.reviewer_id == "reviewer:alice"


def test_approve_decision_links_back_to_item() -> None:
    q = _queue()
    item = _submit(q)
    decision = _decide(q, item, ReviewOutcome.APPROVE)
    assert decision.review_item_id == item.review_item_id
    assert decision.output_id == item.output_id


def test_approve_decision_has_decided_at_timestamp() -> None:
    q = _queue()
    item = _submit(q)
    before = datetime.now(tz=UTC)
    decision = _decide(q, item, ReviewOutcome.APPROVE)
    after = datetime.now(tz=UTC)
    assert before <= decision.decided_at <= after


def test_approve_decision_id_is_sixteen_hex_chars() -> None:
    q = _queue()
    item = _submit(q)
    decision = _decide(q, item, ReviewOutcome.APPROVE)
    assert len(decision.decision_id) == 16
    assert all(c in "0123456789abcdef" for c in decision.decision_id)


def test_approve_decision_carries_full_reviewer_profile() -> None:
    q = _queue()
    item = _submit(q)
    decision = _decide(q, item, ReviewOutcome.APPROVE)
    assert decision.reviewer_profile.max_authority is AuthorityOutcome.A2
    assert "eu-west-1" in decision.reviewer_profile.permitted_regions


# ---------------------------------------------------------------------------
# ReviewQueue.decide -- REJECT
# ---------------------------------------------------------------------------


def test_reject_transitions_item_to_rejected() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.REJECT, rationale="Weak citation")
    assert q.status(item.review_item_id) is ReviewStatus.REJECTED


def test_rejected_item_leaves_queue_empty() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.REJECT)
    assert q.pop() is None


def test_reject_decision_carries_rationale() -> None:
    q = _queue()
    item = _submit(q)
    decision = _decide(q, item, ReviewOutcome.REJECT, rationale="Rule unclear")
    assert decision.rationale == "Rule unclear"


# ---------------------------------------------------------------------------
# ReviewQueue.decide -- ESCALATE
# ---------------------------------------------------------------------------


def test_escalate_transitions_item_to_escalated() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.ESCALATE)
    assert q.status(item.review_item_id) is ReviewStatus.ESCALATED


def test_escalated_item_can_be_decided_again() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.ESCALATE)
    _decide(q, item, ReviewOutcome.APPROVE)
    assert q.status(item.review_item_id) is ReviewStatus.APPROVED


def test_escalate_then_reject_stores_two_decisions() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.ESCALATE)
    _decide(q, item, ReviewOutcome.REJECT)
    decs = q.decisions(item.review_item_id)
    assert len(decs) == 2
    assert decs[0].outcome is ReviewOutcome.ESCALATE
    assert decs[1].outcome is ReviewOutcome.REJECT


def test_escalated_item_increments_escalated_count() -> None:
    q = _queue()
    item = _submit(q)
    assert q.escalated_count() == 0
    _decide(q, item, ReviewOutcome.ESCALATE)
    assert q.escalated_count() == 1


def test_escalate_decision_has_unique_id_from_subsequent_approve() -> None:
    q = _queue()
    item = _submit(q)
    d1 = _decide(q, item, ReviewOutcome.ESCALATE)
    d2 = _decide(q, item, ReviewOutcome.APPROVE)
    assert d1.decision_id != d2.decision_id


# ---------------------------------------------------------------------------
# ReviewQueue.decide -- MODIFY
# ---------------------------------------------------------------------------


def test_modify_transitions_item_to_modified() -> None:
    q = _queue()
    item = _submit(q)
    _decide(
        q, item, ReviewOutcome.MODIFY,
        correction="EU-VAT exemption applies under article 135(1)(a)",
    )
    assert q.status(item.review_item_id) is ReviewStatus.MODIFIED


def test_modified_status_is_terminal() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.MODIFY, correction="Corrected classification")
    with pytest.raises(ReviewError, match="terminal"):
        _decide(q, item, ReviewOutcome.APPROVE)


def test_modify_decision_stores_correction_on_record() -> None:
    q = _queue()
    item = _submit(q)
    decision = _decide(q, item, ReviewOutcome.MODIFY, correction="Amended: rate 20% not 5%")
    assert decision.correction == "Amended: rate 20% not 5%"


def test_modify_without_correction_string_is_refused_in_queue() -> None:
    q = _queue()
    item = _submit(q)
    with pytest.raises(ReviewError, match="non-empty correction"):
        q.decide(
            review_item_id=item.review_item_id,
            reviewer_profile=BASE_REVIEWER_PROFILE,
            outcome=ReviewOutcome.MODIFY,
            correction="",
        )


def test_modify_decision_carries_full_reviewer_profile() -> None:
    q = _queue()
    item = _submit(q)
    decision = _decide(q, item, ReviewOutcome.MODIFY, correction="Fixed output")
    assert decision.reviewer_profile.reviewer_id == "reviewer:alice"


def test_modified_item_leaves_queue_empty() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.MODIFY, correction="Corrected")
    assert q.pop() is None


# ---------------------------------------------------------------------------
# ReviewQueue.decide -- terminal-state guard
# ---------------------------------------------------------------------------


def test_decide_on_approved_item_is_refused() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.APPROVE)
    with pytest.raises(ReviewError, match="terminal"):
        _decide(q, item, ReviewOutcome.REJECT)


def test_decide_on_rejected_item_is_refused() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.REJECT)
    with pytest.raises(ReviewError, match="terminal"):
        _decide(q, item, ReviewOutcome.APPROVE)


def test_decide_on_unknown_item_is_refused() -> None:
    q = _queue()
    with pytest.raises(ReviewError, match="unknown"):
        q.decide("nonexistent0000a1", BASE_REVIEWER_PROFILE, ReviewOutcome.APPROVE)


# ---------------------------------------------------------------------------
# ReviewQueue.decide -- reviewer qualification checks
# ---------------------------------------------------------------------------


def test_reviewer_from_wrong_region_is_refused() -> None:
    q = _queue()
    item = _submit(q)
    wrong_region_profile = replace(
        BASE_REVIEWER_PROFILE,
        permitted_regions=frozenset({"us-east-1"}),
    )
    with pytest.raises(ReviewError, match="not permitted to review in region"):
        _decide(q, item, ReviewOutcome.APPROVE, reviewer_profile=wrong_region_profile)


def test_reviewer_with_insufficient_authority_is_refused() -> None:
    q = _queue()
    item = _submit(q)
    low_authority_profile = replace(
        BASE_REVIEWER_PROFILE,
        max_authority=AuthorityOutcome.A0,
    )
    with pytest.raises(ReviewError, match="max authority"):
        _decide(q, item, ReviewOutcome.APPROVE, reviewer_profile=low_authority_profile)


def test_reviewer_with_exact_matching_authority_is_accepted() -> None:
    q = _queue()
    item = _submit(q)
    a1_profile = replace(BASE_REVIEWER_PROFILE, max_authority=AuthorityOutcome.A1)
    decision = _decide(q, item, ReviewOutcome.APPROVE, reviewer_profile=a1_profile)
    assert decision.outcome is ReviewOutcome.APPROVE


def test_reviewer_with_higher_authority_is_accepted() -> None:
    q = _queue()
    item = _submit(q)
    senior_profile = replace(BASE_REVIEWER_PROFILE, max_authority=AuthorityOutcome.A3)
    decision = _decide(q, item, ReviewOutcome.APPROVE, reviewer_profile=senior_profile)
    assert decision.outcome is ReviewOutcome.APPROVE


def test_reviewer_region_check_applies_to_modify_too() -> None:
    q = _queue()
    item = _submit(q)
    wrong_region_profile = replace(
        BASE_REVIEWER_PROFILE,
        permitted_regions=frozenset({"us-east-1"}),
    )
    with pytest.raises(ReviewError, match="not permitted to review in region"):
        _decide(
            q, item, ReviewOutcome.MODIFY,
            correction="Fixed output",
            reviewer_profile=wrong_region_profile,
        )


def test_reviewer_authority_check_applies_to_modify_too() -> None:
    q = _queue()
    item = _submit(q)
    low_profile = replace(BASE_REVIEWER_PROFILE, max_authority=AuthorityOutcome.A0)
    with pytest.raises(ReviewError, match="max authority"):
        _decide(
            q, item, ReviewOutcome.MODIFY,
            correction="Fixed output",
            reviewer_profile=low_profile,
        )


def test_reviewer_authority_check_applies_to_escalate_too() -> None:
    q = _queue()
    item = _submit(q)
    low_profile = replace(BASE_REVIEWER_PROFILE, max_authority=AuthorityOutcome.A0)
    with pytest.raises(ReviewError, match="max authority"):
        _decide(q, item, ReviewOutcome.ESCALATE, reviewer_profile=low_profile)


# ---------------------------------------------------------------------------
# ReviewQueue.pop -- FIFO ordering
# ---------------------------------------------------------------------------


def test_pop_returns_oldest_item_first() -> None:
    q = _queue()
    first = _submit(q, output_id="first", citation=_citation(content=b"c1"))
    _submit(q, output_id="second", citation=_citation(content=b"c2"))
    assert q.pop() is first


def test_pop_skips_decided_items() -> None:
    q = _queue()
    item1 = _submit(q, output_id="out-1", citation=_citation(content=b"c1"))
    item2 = _submit(q, output_id="out-2", citation=_citation(content=b"c2"))
    _decide(q, item1, ReviewOutcome.APPROVE)
    assert q.pop() is item2


def test_pop_on_empty_queue_returns_none() -> None:
    assert _queue().pop() is None


def test_pop_on_all_decided_queue_returns_none() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.APPROVE)
    assert q.pop() is None


def test_pop_does_not_change_item_status() -> None:
    q = _queue()
    item = _submit(q)
    q.pop()
    assert q.status(item.review_item_id) is ReviewStatus.PENDING


# ---------------------------------------------------------------------------
# ReviewQueue.get
# ---------------------------------------------------------------------------


def test_get_returns_item_for_known_id() -> None:
    q = _queue()
    item = _submit(q)
    assert q.get(item.review_item_id) is item


def test_get_returns_none_for_unknown_id() -> None:
    assert _queue().get("does-not-exist0") is None


# ---------------------------------------------------------------------------
# ReviewQueue.status
# ---------------------------------------------------------------------------


def test_status_returns_pending_for_new_item() -> None:
    q = _queue()
    item = _submit(q)
    assert q.status(item.review_item_id) is ReviewStatus.PENDING


def test_status_returns_approved_after_approve() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.APPROVE)
    assert q.status(item.review_item_id) is ReviewStatus.APPROVED


def test_status_returns_rejected_after_reject() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.REJECT)
    assert q.status(item.review_item_id) is ReviewStatus.REJECTED


def test_status_returns_escalated_after_escalate() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.ESCALATE)
    assert q.status(item.review_item_id) is ReviewStatus.ESCALATED


def test_status_returns_modified_after_modify() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.MODIFY, correction="Fixed")
    assert q.status(item.review_item_id) is ReviewStatus.MODIFIED


def test_status_returns_none_for_unknown_item() -> None:
    assert _queue().status("unknown-item-id0") is None


# ---------------------------------------------------------------------------
# ReviewQueue.decisions
# ---------------------------------------------------------------------------


def test_decisions_is_empty_before_any_decision() -> None:
    q = _queue()
    item = _submit(q)
    assert q.decisions(item.review_item_id) == []


def test_decisions_contains_one_entry_after_approve() -> None:
    q = _queue()
    item = _submit(q)
    d = _decide(q, item, ReviewOutcome.APPROVE)
    assert q.decisions(item.review_item_id) == [d]


def test_decisions_are_ordered_chronologically() -> None:
    q = _queue()
    item = _submit(q)
    d1 = _decide(q, item, ReviewOutcome.ESCALATE)
    d2 = _decide(q, item, ReviewOutcome.APPROVE)
    decs = q.decisions(item.review_item_id)
    assert decs[0] is d1
    assert decs[1] is d2


def test_decisions_returns_empty_list_for_unknown_item() -> None:
    assert _queue().decisions("unknown-item-id0") == []


def test_decisions_returns_a_copy_not_the_internal_list() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.APPROVE)
    copy = q.decisions(item.review_item_id)
    copy.clear()
    assert len(q.decisions(item.review_item_id)) == 1


# ---------------------------------------------------------------------------
# ReviewQueue counts
# ---------------------------------------------------------------------------


def test_pending_count_is_zero_for_empty_queue() -> None:
    assert _queue().pending_count() == 0


def test_pending_count_decreases_after_approve() -> None:
    q = _queue()
    item = _submit(q)
    assert q.pending_count() == 1
    _decide(q, item, ReviewOutcome.APPROVE)
    assert q.pending_count() == 0


def test_pending_count_decreases_after_reject() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.REJECT)
    assert q.pending_count() == 0


def test_pending_count_decreases_after_escalate() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.ESCALATE)
    assert q.pending_count() == 0


def test_pending_count_decreases_after_modify() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.MODIFY, correction="Fixed")
    assert q.pending_count() == 0


def test_escalated_count_is_zero_initially() -> None:
    assert _queue().escalated_count() == 0


def test_escalated_count_increases_after_escalate() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.ESCALATE)
    assert q.escalated_count() == 1


def test_escalated_count_decreases_after_final_decision() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.ESCALATE)
    _decide(q, item, ReviewOutcome.APPROVE)
    assert q.escalated_count() == 0


# ---------------------------------------------------------------------------
# TTL / EXPIRED
# ---------------------------------------------------------------------------


def test_item_past_ttl_becomes_expired_on_pop() -> None:
    q = ReviewQueue(ttl_seconds=0)
    item = _submit(q)
    time.sleep(0.01)
    result = q.pop()
    assert result is None
    assert q.status(item.review_item_id) is ReviewStatus.EXPIRED


def test_item_past_ttl_becomes_expired_on_pending_count() -> None:
    q = ReviewQueue(ttl_seconds=0)
    _submit(q)
    time.sleep(0.01)
    assert q.pending_count() == 0


def test_expired_item_cannot_be_decided() -> None:
    q = ReviewQueue(ttl_seconds=0)
    item = _submit(q)
    time.sleep(0.01)
    q.pop()
    with pytest.raises(ReviewError, match="terminal"):
        _decide(q, item, ReviewOutcome.APPROVE)


def test_item_within_ttl_is_not_expired() -> None:
    q = ReviewQueue(ttl_seconds=3600)
    item = _submit(q)
    q.pop()
    assert q.status(item.review_item_id) is ReviewStatus.PENDING


def test_no_ttl_means_item_never_expires() -> None:
    q = ReviewQueue(ttl_seconds=None)
    item = _submit(q)
    time.sleep(0.01)
    q.pop()
    assert q.status(item.review_item_id) is ReviewStatus.PENDING


# ---------------------------------------------------------------------------
# Full audit trail -- integration
# ---------------------------------------------------------------------------


def test_full_audit_trail_item_then_approve() -> None:
    q = _queue()
    cit = _citation(content=b"VAT directive article 135(1)(a)")
    item = q.submit(
        output_id="report-abc123",
        citation=cit,
        summary="EU-VAT exemption classification for SIM card service",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
    )
    decision = q.decide(
        item.review_item_id,
        reviewer_profile=BASE_REVIEWER_PROFILE,
        outcome=ReviewOutcome.APPROVE,
        rationale="Matches article 135(1)(a) -- exempt financial service",
    )

    item_d = item.as_dict()
    assert item_d["output_id"] == "report-abc123"
    assert item_d["citation_id"] == cit.citation_id
    assert item_d["status"] == "PENDING"

    dec_d = decision.as_dict()
    assert dec_d["review_item_id"] == item.review_item_id
    assert dec_d["output_id"] == "report-abc123"
    assert dec_d["reviewer_id"] == "reviewer:alice"
    assert dec_d["outcome"] == "APPROVE"
    assert dec_d["rationale"] == "Matches article 135(1)(a) -- exempt financial service"
    assert isinstance(dec_d["reviewer_profile"], dict)

    assert q.status(item.review_item_id) is ReviewStatus.APPROVED
    assert q.pop() is None


def test_full_audit_trail_escalate_then_reject() -> None:
    q = _queue()
    item = _submit(q)
    d1 = _decide(q, item, ReviewOutcome.ESCALATE, rationale="Requires senior counsel")
    d2 = _decide(q, item, ReviewOutcome.REJECT, rationale="Not covered by jurisdiction")
    decs = q.decisions(item.review_item_id)
    assert len(decs) == 2
    assert decs[0] is d1
    assert decs[1] is d2
    assert q.status(item.review_item_id) is ReviewStatus.REJECTED


def test_full_audit_trail_modify_correction() -> None:
    q = _queue()
    item = _submit(q, summary="Rate classification: SIM card VAT at 5%")
    decision = q.decide(
        item.review_item_id,
        reviewer_profile=BASE_REVIEWER_PROFILE,
        outcome=ReviewOutcome.MODIFY,
        rationale="Rate is 20% not 5%; article 96 applies",
        correction="Amended: EU-VAT standard rate 20% applies to SIM card sale",
    )
    assert q.status(item.review_item_id) is ReviewStatus.MODIFIED
    dec_d = decision.as_dict()
    assert dec_d["outcome"] == "MODIFY"
    assert dec_d["correction"] == "Amended: EU-VAT standard rate 20% applies to SIM card sale"
    assert dec_d["rationale"] == "Rate is 20% not 5%; article 96 applies"
    profile_d = dec_d["reviewer_profile"]
    assert isinstance(profile_d, dict)
    assert profile_d["max_authority"] == "A2"


def test_multiple_items_in_queue_maintain_independent_state() -> None:
    q = _queue()
    item_a = _submit(q, output_id="out-a", citation=_citation(content=b"cite-a"))
    item_b = _submit(q, output_id="out-b", citation=_citation(content=b"cite-b"))
    item_c = _submit(q, output_id="out-c", citation=_citation(content=b"cite-c"))
    _decide(q, item_b, ReviewOutcome.REJECT)
    assert q.status(item_a.review_item_id) is ReviewStatus.PENDING
    assert q.status(item_b.review_item_id) is ReviewStatus.REJECTED
    assert q.status(item_c.review_item_id) is ReviewStatus.PENDING
    assert q.pending_count() == 2
    assert q.pop() is item_a


# ===========================================================================
# EvidencePanel
# ===========================================================================


def test_evidence_panel_diverges_when_answers_differ() -> None:
    panel = EvidencePanel(ai_answer="VAT 20%", rule_answer="VAT 5%")
    assert panel.diverges is True


def test_evidence_panel_not_diverges_when_identical() -> None:
    panel = EvidencePanel(ai_answer="VAT 20%", rule_answer="VAT 20%")
    assert panel.diverges is False


def test_evidence_panel_case_insensitive_comparison() -> None:
    panel = EvidencePanel(ai_answer="  VAT 20%  ", rule_answer="vat 20%")
    assert panel.diverges is False


def test_evidence_panel_empty_rule_answer_treated_as_no_deterministic_answer() -> None:
    panel = EvidencePanel(ai_answer="VAT 20%", rule_answer="")
    assert panel.diverges is True


def test_evidence_panel_rejects_empty_ai_answer() -> None:
    with pytest.raises(ReviewError, match="ai_answer is required"):
        EvidencePanel(ai_answer="", rule_answer="VAT 5%")


def test_evidence_panel_as_dict() -> None:
    panel = EvidencePanel(
        ai_answer="VAT 20%",
        rule_answer="VAT 20%",
        divergence_detail="none",
    )
    d = panel.as_dict()
    assert d["ai_answer"] == "VAT 20%"
    assert d["diverges"] is False
    assert d["divergence_detail"] == "none"


def test_evidence_panel_immutable() -> None:
    from dataclasses import FrozenInstanceError
    panel = EvidencePanel(ai_answer="A", rule_answer="B")
    with pytest.raises(FrozenInstanceError):
        panel.ai_answer = "X"  # type: ignore[misc]


def test_evidence_panel_attached_to_review_item_via_submit() -> None:
    q = _queue()
    panel = EvidencePanel(ai_answer="AI: 20%", rule_answer="Rule: 5%")
    item = q.submit(
        output_id="out-ep-1",
        citation=_citation(),
        summary="Check VAT rate",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        evidence_panel=panel,
    )
    assert item.evidence_panel is panel
    assert item.evidence_panel.diverges is True
    d = item.as_dict()
    assert "evidence_panel" in d
    assert d["evidence_panel"]["diverges"] is True  # type: ignore[index]


def test_review_item_without_evidence_panel_has_none() -> None:
    q = _queue()
    item = _submit(q)
    assert item.evidence_panel is None
    assert "evidence_panel" not in item.as_dict()


# ===========================================================================
# ReviewPriority and SLA (due_by)
# ===========================================================================


def test_review_priority_enum_values() -> None:
    assert ReviewPriority.CRITICAL == "CRITICAL"
    assert ReviewPriority.HIGH == "HIGH"
    assert ReviewPriority.NORMAL == "NORMAL"
    assert ReviewPriority.LOW == "LOW"


def test_review_priority_urgency_ordering() -> None:
    assert ReviewPriority.CRITICAL.urgency() > ReviewPriority.HIGH.urgency()
    assert ReviewPriority.HIGH.urgency() > ReviewPriority.NORMAL.urgency()
    assert ReviewPriority.NORMAL.urgency() > ReviewPriority.LOW.urgency()


def test_review_priority_sla_breached_when_due_by_passed() -> None:
    from datetime import timedelta
    past = datetime.now(tz=UTC) - timedelta(hours=1)
    assert ReviewPriority.CRITICAL.is_sla_breached(past) is True
    assert ReviewPriority.HIGH.is_sla_breached(past) is True
    assert ReviewPriority.NORMAL.is_sla_breached(past) is True


def test_review_priority_low_never_breaches_sla() -> None:
    from datetime import timedelta
    past = datetime.now(tz=UTC) - timedelta(hours=100)
    assert ReviewPriority.LOW.is_sla_breached(past) is False


def test_review_priority_sla_not_breached_when_due_by_future() -> None:
    from datetime import timedelta
    future = datetime.now(tz=UTC) + timedelta(hours=24)
    assert ReviewPriority.CRITICAL.is_sla_breached(future) is False


def test_review_priority_sla_not_breached_when_no_due_by() -> None:
    assert ReviewPriority.CRITICAL.is_sla_breached(None) is False


def test_review_item_default_priority_is_normal() -> None:
    q = _queue()
    item = _submit(q)
    assert item.priority is ReviewPriority.NORMAL
    assert item.due_by is None


def test_review_item_accepts_priority_and_due_by() -> None:
    from datetime import timedelta
    future = datetime.now(tz=UTC) + timedelta(hours=4)
    q = _queue()
    item = q.submit(
        output_id="out-critical",
        citation=_citation(),
        summary="Critical GST ruling",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        priority=ReviewPriority.CRITICAL,
        due_by=future,
    )
    assert item.priority is ReviewPriority.CRITICAL
    assert item.due_by == future
    assert item.sla_breached is False


def test_review_item_sla_breached_property() -> None:
    from datetime import timedelta
    past = datetime.now(tz=UTC) - timedelta(seconds=1)
    q = _queue()
    item = q.submit(
        output_id="out-overdue",
        citation=_citation(),
        summary="Overdue item",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        priority=ReviewPriority.HIGH,
        due_by=past,
    )
    assert item.sla_breached is True
    d = item.as_dict()
    assert d["sla_breached"] is True
    assert "due_by" in d


def test_review_item_rejects_naive_due_by() -> None:
    naive = datetime(2026, 12, 31, 12, 0, 0)  # no tzinfo
    with pytest.raises(ReviewError, match="timezone-aware"):
        ReviewItem(
            output_id="out-naive",
            citation=_citation(),
            summary="summary",
            provenance=BASE_PROVENANCE,
            due_by=naive,
        )


def test_review_item_as_dict_includes_priority() -> None:
    q = _queue()
    item = q.submit(
        output_id="out-prio",
        citation=_citation(),
        summary="priority test",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        priority=ReviewPriority.HIGH,
    )
    d = item.as_dict()
    assert d["priority"] == "HIGH"
    assert "due_by" not in d


# ===========================================================================
# DecisionReasonCode
# ===========================================================================


def test_decision_reason_code_enum_values() -> None:
    assert DecisionReasonCode.FACTUALLY_CORRECT == "FACTUALLY_CORRECT"
    assert DecisionReasonCode.FACTUALLY_INCORRECT == "FACTUALLY_INCORRECT"
    assert DecisionReasonCode.CORRECTION_REQUIRED == "CORRECTION_REQUIRED"
    assert DecisionReasonCode.OTHER == "OTHER"


def test_decide_records_reason_code() -> None:
    q = _queue()
    item = _submit(q)
    decision = q.decide(
        review_item_id=item.review_item_id,
        reviewer_profile=BASE_REVIEWER_PROFILE,
        outcome=ReviewOutcome.APPROVE,
        reason_code=DecisionReasonCode.FACTUALLY_CORRECT,
    )
    assert decision.reason_code is DecisionReasonCode.FACTUALLY_CORRECT
    d = decision.as_dict()
    assert d["reason_code"] == "FACTUALLY_CORRECT"


def test_decide_without_reason_code_is_none() -> None:
    q = _queue()
    item = _submit(q)
    decision = q.decide(
        review_item_id=item.review_item_id,
        reviewer_profile=BASE_REVIEWER_PROFILE,
        outcome=ReviewOutcome.APPROVE,
    )
    assert decision.reason_code is None
    assert "reason_code" not in decision.as_dict()


def test_decide_reject_with_reason_code() -> None:
    q = _queue()
    item = _submit(q)
    decision = q.decide(
        review_item_id=item.review_item_id,
        reviewer_profile=BASE_REVIEWER_PROFILE,
        outcome=ReviewOutcome.REJECT,
        rationale="Source does not support the claim",
        reason_code=DecisionReasonCode.CITATION_INSUFFICIENT,
    )
    assert decision.reason_code is DecisionReasonCode.CITATION_INSUFFICIENT
    assert decision.as_dict()["reason_code"] == "CITATION_INSUFFICIENT"


def test_decide_escalate_with_reason_code() -> None:
    q = _queue()
    item = _submit(q)
    decision = q.decide(
        review_item_id=item.review_item_id,
        reviewer_profile=BASE_REVIEWER_PROFILE,
        outcome=ReviewOutcome.ESCALATE,
        reason_code=DecisionReasonCode.REQUIRES_HIGHER_AUTHORITY,
    )
    assert decision.reason_code is DecisionReasonCode.REQUIRES_HIGHER_AUTHORITY


def test_decide_modify_with_reason_code() -> None:
    q = _queue()
    item = _submit(q)
    decision = q.decide(
        review_item_id=item.review_item_id,
        reviewer_profile=BASE_REVIEWER_PROFILE,
        outcome=ReviewOutcome.MODIFY,
        correction="corrected text",
        reason_code=DecisionReasonCode.CORRECTION_REQUIRED,
    )
    assert decision.reason_code is DecisionReasonCode.CORRECTION_REQUIRED


# ===========================================================================
# PromotedRecord
# ===========================================================================


def test_promoted_record_construction() -> None:
    record = PromotedRecord(
        review_item_id="item-1",
        decision_id="dec-1",
        output_id="out-1",
        payload="rule version 2.0",
        promoted_at=datetime.now(tz=UTC),
    )
    assert record.payload == "rule version 2.0"
    assert len(record.promoted_record_id) == 16


def test_promoted_record_deterministic_id() -> None:
    now = datetime.now(tz=UTC)
    r1 = PromotedRecord(
        review_item_id="item-x",
        decision_id="dec-x",
        output_id="out-x",
        payload="payload",
        promoted_at=now,
    )
    r2 = PromotedRecord(
        review_item_id="item-x",
        decision_id="dec-x",
        output_id="out-x",
        payload="different payload",
        promoted_at=now,
    )
    # Same item+decision pair always produces the same record ID.
    assert r1.promoted_record_id == r2.promoted_record_id


def test_promoted_record_rejects_empty_payload() -> None:
    with pytest.raises(ReviewError, match="non-empty payload"):
        PromotedRecord(
            review_item_id="item-1",
            decision_id="dec-1",
            output_id="out-1",
            payload="",
            promoted_at=datetime.now(tz=UTC),
        )


def test_promoted_record_immutable() -> None:
    from dataclasses import FrozenInstanceError
    record = PromotedRecord(
        review_item_id="item-1",
        decision_id="dec-1",
        output_id="out-1",
        payload="payload",
        promoted_at=datetime.now(tz=UTC),
    )
    with pytest.raises(FrozenInstanceError):
        record.payload = "tampered"  # type: ignore[misc]


def test_promoted_record_as_dict() -> None:
    now = datetime.now(tz=UTC)
    record = PromotedRecord(
        review_item_id="item-1",
        decision_id="dec-1",
        output_id="out-1",
        payload="approved diff",
        promoted_at=now,
    )
    d = record.as_dict()
    assert d["payload"] == "approved diff"
    assert d["promoted_at"] == now.isoformat()
    assert len(str(d["promoted_record_id"])) == 16


# ===========================================================================
# ReviewQueue.promote
# ===========================================================================


def test_promote_approved_item_creates_promoted_record() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.APPROVE)
    record = q.promote(item.review_item_id, payload="VAT rule v2")
    assert isinstance(record, PromotedRecord)
    assert record.output_id == item.output_id
    assert record.payload == "VAT rule v2"
    assert q.status(item.review_item_id) is ReviewStatus.PROMOTED


def test_promote_transitions_to_promoted_terminal_status() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.APPROVE)
    q.promote(item.review_item_id, payload="content")
    assert q.status(item.review_item_id) is ReviewStatus.PROMOTED
    # PROMOTED is terminal — decide() must be refused.
    with pytest.raises(ReviewError, match="terminal"):
        _decide(q, item, ReviewOutcome.APPROVE)


def test_promote_prevents_double_promotion() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.APPROVE)
    q.promote(item.review_item_id, payload="v1")
    # Second promote must fail because status is now PROMOTED, not APPROVED.
    with pytest.raises(ReviewError, match="PROMOTED"):
        q.promote(item.review_item_id, payload="v2")


def test_promote_refuses_pending_item() -> None:
    q = _queue()
    item = _submit(q)
    with pytest.raises(ReviewError, match="PENDING"):
        q.promote(item.review_item_id, payload="content")


def test_promote_refuses_rejected_item() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.REJECT)
    with pytest.raises(ReviewError, match="REJECTED"):
        q.promote(item.review_item_id, payload="content")


def test_promote_refuses_escalated_item() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.ESCALATE)
    with pytest.raises(ReviewError, match="ESCALATED"):
        q.promote(item.review_item_id, payload="content")


def test_promote_refuses_modified_item() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.MODIFY, correction="corrected")
    with pytest.raises(ReviewError, match="MODIFIED"):
        q.promote(item.review_item_id, payload="content")


def test_promote_refuses_empty_payload() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.APPROVE)
    with pytest.raises(ReviewError, match="non-empty payload"):
        q.promote(item.review_item_id, payload="")


def test_promote_refuses_unknown_item() -> None:
    q = _queue()
    with pytest.raises(ReviewError, match="unknown"):
        q.promote("no-such-id", payload="content")


def test_promoted_record_retrievable_via_queue() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.APPROVE)
    record = q.promote(item.review_item_id, payload="v1")
    assert q.promoted_record(item.review_item_id) is record


def test_promoted_record_returns_none_for_unknown_item() -> None:
    q = _queue()
    assert q.promoted_record("unknown") is None


def test_promoted_record_returns_none_for_approved_but_not_promoted() -> None:
    q = _queue()
    item = _submit(q)
    _decide(q, item, ReviewOutcome.APPROVE)
    assert q.promoted_record(item.review_item_id) is None


def test_promoted_count_increments_on_promote() -> None:
    q = _queue()
    item_a = _submit(q, output_id="out-a", citation=_citation(content=b"a"))
    item_b = _submit(q, output_id="out-b", citation=_citation(content=b"b"))
    _decide(q, item_a, ReviewOutcome.APPROVE)
    _decide(q, item_b, ReviewOutcome.APPROVE)
    assert q.promoted_count() == 0
    q.promote(item_a.review_item_id, payload="v1")
    assert q.promoted_count() == 1
    q.promote(item_b.review_item_id, payload="v2")
    assert q.promoted_count() == 2


def test_promote_record_decision_id_matches_approve_decision() -> None:
    q = _queue()
    item = _submit(q)
    approve_decision = _decide(q, item, ReviewOutcome.APPROVE)
    record = q.promote(item.review_item_id, payload="payload")
    assert record.decision_id == approve_decision.decision_id


# ===========================================================================
# Priority-aware pop
# ===========================================================================


def test_pop_surfaces_critical_before_normal() -> None:
    q = _queue()
    normal = q.submit(
        output_id="out-normal",
        citation=_citation(content=b"normal"),
        summary="normal",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        priority=ReviewPriority.NORMAL,
    )
    critical = q.submit(
        output_id="out-critical",
        citation=_citation(content=b"critical"),
        summary="critical",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        priority=ReviewPriority.CRITICAL,
    )
    # Inserted in NORMAL, CRITICAL order — but CRITICAL must come first.
    assert q.pop() is critical
    _ = normal  # referenced to satisfy linters


def test_pop_priority_ordering_all_tiers() -> None:
    q = _queue()
    low = q.submit(
        output_id="out-low",
        citation=_citation(content=b"low"),
        summary="low",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        priority=ReviewPriority.LOW,
    )
    high = q.submit(
        output_id="out-high",
        citation=_citation(content=b"high"),
        summary="high",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        priority=ReviewPriority.HIGH,
    )
    normal = q.submit(
        output_id="out-normal2",
        citation=_citation(content=b"normal2"),
        summary="normal",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        priority=ReviewPriority.NORMAL,
    )
    critical = q.submit(
        output_id="out-critical2",
        citation=_citation(content=b"critical2"),
        summary="critical",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        priority=ReviewPriority.CRITICAL,
    )
    assert q.pop() is critical
    _decide(q, critical, ReviewOutcome.APPROVE)
    assert q.pop() is high
    _decide(q, high, ReviewOutcome.APPROVE)
    assert q.pop() is normal
    _decide(q, normal, ReviewOutcome.APPROVE)
    assert q.pop() is low
    _ = low


def test_pop_fifo_within_same_priority_tier() -> None:
    q = _queue()
    first = q.submit(
        output_id="out-first",
        citation=_citation(content=b"first"),
        summary="first high",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        priority=ReviewPriority.HIGH,
    )
    second = q.submit(
        output_id="out-second",
        citation=_citation(content=b"second"),
        summary="second high",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        priority=ReviewPriority.HIGH,
    )
    # Same tier — should respect insertion order.
    assert q.pop() is first
    _ = second


# ===========================================================================
# SLA breach detection — sla_breached_items
# ===========================================================================


def test_sla_breached_items_empty_when_none_overdue() -> None:
    from datetime import timedelta
    future = datetime.now(tz=UTC) + timedelta(hours=24)
    q = _queue()
    q.submit(
        output_id="out-future",
        citation=_citation(),
        summary="future",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        priority=ReviewPriority.HIGH,
        due_by=future,
    )
    assert q.sla_breached_items() == []


def test_sla_breached_items_returns_overdue_pending() -> None:
    from datetime import timedelta
    past = datetime.now(tz=UTC) - timedelta(seconds=1)
    q = _queue()
    overdue = q.submit(
        output_id="out-overdue",
        citation=_citation(),
        summary="overdue",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        priority=ReviewPriority.HIGH,
        due_by=past,
    )
    breached = q.sla_breached_items()
    assert len(breached) == 1
    assert breached[0].review_item_id == overdue.review_item_id


def test_sla_breached_items_excludes_low_priority() -> None:
    from datetime import timedelta
    past = datetime.now(tz=UTC) - timedelta(hours=100)
    q = _queue()
    q.submit(
        output_id="out-low-overdue",
        citation=_citation(),
        summary="low priority no deadline",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        priority=ReviewPriority.LOW,
        due_by=past,
    )
    assert q.sla_breached_items() == []


def test_sla_breached_items_excludes_non_pending() -> None:
    from datetime import timedelta
    past = datetime.now(tz=UTC) - timedelta(seconds=1)
    q = _queue()
    item = q.submit(
        output_id="out-approved-overdue",
        citation=_citation(),
        summary="approved",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        priority=ReviewPriority.CRITICAL,
        due_by=past,
    )
    _decide(q, item, ReviewOutcome.APPROVE)
    # No longer PENDING — must not appear in breached list.
    assert q.sla_breached_items() == []


def test_sla_breached_items_sorted_most_overdue_first() -> None:
    from datetime import timedelta
    q = _queue()
    later_past = datetime.now(tz=UTC) - timedelta(hours=1)
    earlier_past = datetime.now(tz=UTC) - timedelta(hours=5)
    q.submit(
        output_id="out-less-overdue",
        citation=_citation(content=b"less"),
        summary="less overdue",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        priority=ReviewPriority.HIGH,
        due_by=later_past,
    )
    q.submit(
        output_id="out-more-overdue",
        citation=_citation(content=b"more"),
        summary="more overdue",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        priority=ReviewPriority.CRITICAL,
        due_by=earlier_past,
    )
    breached = q.sla_breached_items()
    assert len(breached) == 2
    # Most overdue (5 h ago) comes first.
    assert breached[0].due_by == earlier_past
    assert breached[1].due_by == later_past


# ===========================================================================
# Quality sampling — sample_approved
# ===========================================================================


def test_sample_approved_empty_when_no_approvals() -> None:
    q = _queue()
    _submit(q)  # PENDING, not approved
    result = q.sample_approved(k=3)
    assert result == []


def test_sample_approved_returns_up_to_k() -> None:
    q = _queue()
    for i in range(5):
        item = _submit(q, output_id=f"out-{i}", citation=_citation(content=f"c{i}".encode()))
        _decide(q, item, ReviewOutcome.APPROVE)
    rng = random.Random(42)
    result = q.sample_approved(k=3, rng=rng)
    assert len(result) == 3
    assert all(d.outcome is ReviewOutcome.APPROVE for d in result)


def test_sample_approved_returns_all_when_pool_smaller_than_k() -> None:
    q = _queue()
    for i in range(2):
        item = _submit(q, output_id=f"out-sm-{i}", citation=_citation(content=f"sm{i}".encode()))
        _decide(q, item, ReviewOutcome.APPROVE)
    result = q.sample_approved(k=10)
    assert len(result) == 2


def test_sample_approved_excludes_reject_decisions() -> None:
    q = _queue()
    approved = _submit(q, output_id="out-app", citation=_citation(content=b"app"))
    rejected = _submit(q, output_id="out-rej", citation=_citation(content=b"rej"))
    _decide(q, approved, ReviewOutcome.APPROVE)
    _decide(q, rejected, ReviewOutcome.REJECT)
    result = q.sample_approved(k=10)
    assert len(result) == 1
    assert result[0].outcome is ReviewOutcome.APPROVE
    _ = rejected


def test_sample_approved_reproducible_with_seeded_rng() -> None:
    q = _queue()
    for i in range(6):
        item = _submit(q, output_id=f"out-r{i}", citation=_citation(content=f"r{i}".encode()))
        _decide(q, item, ReviewOutcome.APPROVE)
    rng_a = random.Random(99)
    rng_b = random.Random(99)
    assert [d.decision_id for d in q.sample_approved(k=4, rng=rng_a)] == [
        d.decision_id for d in q.sample_approved(k=4, rng=rng_b)
    ]


def test_sample_approved_refuses_k_less_than_one() -> None:
    q = _queue()
    with pytest.raises(ReviewError, match="k >= 1"):
        q.sample_approved(k=0)


# ===========================================================================
# Integration: full pipeline — submit -> decide(APPROVE) -> promote
# ===========================================================================


def test_integration_full_pipeline_approve_and_promote() -> None:
    """End-to-end: submit with EvidencePanel, approve with reason code, promote."""
    from datetime import timedelta
    q = _queue()
    panel = EvidencePanel(ai_answer="VAT 20%", rule_answer="VAT 5%")
    due = datetime.now(tz=UTC) + timedelta(hours=4)
    item = q.submit(
        output_id="out-full",
        citation=_citation(),
        summary="Check VAT rate for EU SIM card",
        provenance=BASE_PROVENANCE,
        registry=_registry(),
        evidence_panel=panel,
        priority=ReviewPriority.HIGH,
        due_by=due,
    )
    assert item.evidence_panel.diverges is True  # type: ignore[union-attr]
    assert item.priority is ReviewPriority.HIGH
    assert q.status(item.review_item_id) is ReviewStatus.PENDING

    decision = q.decide(
        review_item_id=item.review_item_id,
        reviewer_profile=BASE_REVIEWER_PROFILE,
        outcome=ReviewOutcome.APPROVE,
        rationale="Source confirms 20% is correct after 2025 amendment",
        reason_code=DecisionReasonCode.FACTUALLY_CORRECT,
    )
    assert decision.reason_code is DecisionReasonCode.FACTUALLY_CORRECT
    assert q.status(item.review_item_id) is ReviewStatus.APPROVED

    record = q.promote(item.review_item_id, payload="rule:eu-vat:rate:20pct:v2")
    assert record.payload == "rule:eu-vat:rate:20pct:v2"
    assert q.status(item.review_item_id) is ReviewStatus.PROMOTED
    assert q.promoted_count() == 1
    assert q.promoted_record(item.review_item_id) is record
    assert q.pending_count() == 0


def test_integration_promote_then_sample_approved() -> None:
    """Promoted items still contribute their APPROVE decisions to the sampling pool."""
    q = _queue()
    for i in range(3):
        item = _submit(q, output_id=f"out-s{i}", citation=_citation(content=f"s{i}".encode()))
        _decide(q, item, ReviewOutcome.APPROVE)
        q.promote(item.review_item_id, payload=f"payload-{i}")
    assert q.promoted_count() == 3
    rng = random.Random(7)
    sample = q.sample_approved(k=2, rng=rng)
    assert len(sample) == 2
    assert all(d.outcome is ReviewOutcome.APPROVE for d in sample)
