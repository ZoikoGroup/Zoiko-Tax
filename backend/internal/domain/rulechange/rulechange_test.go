package rulechange_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rulechange"
)

var (
	proposer    = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-000000000001"))
	reviewer    = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-000000000002"))
	approver    = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-000000000003"))
	at          = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	baseVersion = content.Version{Major: 3, Minor: 2}
)

func proposal(class content.AuthorityClass) rulechange.Proposal {
	p := rulechange.Proposal{
		Pack: "US-NY-TELECOM", BaseVersion: baseVersion, Jurisdiction: "US-NY", ChangeType: rulechange.ChangeRate,
		Summary: "s", Detail: "d", Citation: rulechange.Citation{Source: "NY-186-C", Reference: "§1", Edition: "2026", Authority: class},
		EffectiveFrom: at.AddDate(0, 2, 0), CreatedAt: at, CreatedBy: proposer,
	}
	f, err := rulechange.Fold(p, rulechange.Open(p, nil))
	if err != nil {
		panic(err)
	}
	return f
}

func step(t *testing.T, p rulechange.Proposal, e rulechange.Event, err error) rulechange.Proposal {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	f, ferr := rulechange.Fold(p, append(append([]rulechange.Event(nil), p.History...), e))
	if ferr != nil {
		t.Fatal(ferr)
	}
	return f
}

func TestValidateNeedsSourceAndDates(t *testing.T) {
	p := proposal(content.AuthorityBindingPrimary)
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	q := p
	q.Citation.Reference = ""
	if err := q.Validate(); errs.ReasonOf(err) != errs.ReasonMissingField {
		t.Fatalf("no citation: %v", err)
	}
	q = p
	q.EffectiveFrom = time.Time{}
	if err := q.Validate(); errs.ReasonOf(err) != errs.ReasonMissingField {
		t.Fatalf("no effective date: %v", err)
	}
}

// WF-02 and CONT-001 §8: the proposer neither reviews nor approves; the
// recommender does not approve; approval needs test evidence and A0/A1.
func TestReviewIsIndependentAndEndsAtReleaseEligibility(t *testing.T) {
	p := proposal(content.AuthorityBindingPrimary)
	if _, err := p.Transition(rulechange.StatusRecommended, "ok", 1, proposer, at); errs.ReasonOf(err) != rulechange.ReasonMakerChecker {
		t.Fatalf("self-review: %v", err)
	}
	if _, err := p.Transition(rulechange.StatusRecommended, "ok", 1, reviewer, at); errs.ReasonOf(err) != rulechange.ReasonTestEvidenceRequired {
		t.Fatalf("no evidence: %v", err)
	}
	e, err := p.RecordTestEvidence(rulechange.TestEvidence{Ref: "GS-1"}, 1, proposer, at)
	p = step(t, p, e, err)
	e, err = p.Transition(rulechange.StatusRecommended, "ok", 2, reviewer, at)
	p = step(t, p, e, err)
	if _, err := p.RecordTestEvidence(rulechange.TestEvidence{Ref: "GS-2"}, 3, proposer, at); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("evidence after recommendation: %v", err)
	}
	for who, want := range map[id.UserID]errs.ReasonCode{proposer: rulechange.ReasonMakerChecker, reviewer: rulechange.ReasonMakerChecker} {
		if _, err := p.Transition(rulechange.StatusApprovedForRelease, "ok", 3, who, at); errs.ReasonOf(err) != want {
			t.Fatalf("approval by %s: %v", who, err)
		}
	}
	e, err = p.Transition(rulechange.StatusApprovedForRelease, "ok", 3, approver, at)
	p = step(t, p, e, err)
	if p.Status != rulechange.StatusApprovedForRelease {
		t.Fatalf("status %s", p.Status)
	}
	if _, err := p.Transition(rulechange.StatusWithdrawn, "x", 4, proposer, at); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("withdraw after approval: %v", err)
	}
}

func TestSecondaryAuthorityCannotBeApproved(t *testing.T) {
	p := proposal(content.AuthorityLicensedSpecialist)
	e, err := p.RecordTestEvidence(rulechange.TestEvidence{Ref: "GS-1"}, 1, proposer, at)
	p = step(t, p, e, err)
	e, err = p.Transition(rulechange.StatusRecommended, "ok", 2, reviewer, at)
	p = step(t, p, e, err)
	if _, err := p.Transition(rulechange.StatusApprovedForRelease, "ok", 3, approver, at); errs.ReasonOf(err) != rulechange.ReasonSourceAuthorityInsufficient {
		t.Fatalf("B1 approval: %v", err)
	}
}

func TestOnlyTheProposerWithdrawsAndChangesRequestedIsSuperseded(t *testing.T) {
	p := proposal(content.AuthorityBindingPrimary)
	if _, err := p.Transition(rulechange.StatusWithdrawn, "x", 1, reviewer, at); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("withdraw by reviewer: %v", err)
	}
	if _, err := p.Supersede(id.RuleChangeID{}, proposer, at); err == nil {
		t.Fatal("superseded a PROPOSED change")
	}
	e, err := p.Transition(rulechange.StatusChangesRequested, "cite the regulation", 1, reviewer, at)
	p = step(t, p, e, err)
	if _, err := p.Supersede(id.RuleChangeID{}, proposer, at); err != nil {
		t.Fatal(err)
	}
}
