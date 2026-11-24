package crosswalk_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/classification"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/crosswalk"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

var (
	maker   = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-000000000001"))
	checker = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-000000000002"))
	day     = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
)

func exact(code, target string) crosswalk.Entry {
	return crosswalk.Entry{ExternalCode: code, Description: "d", Match: crosswalk.MatchExact, ServiceTarget: classification.OntologyID("ontology:" + classTarget(target))}
}

func classTarget(s string) string { return "telecom/" + s }

func version(entries ...crosswalk.Entry) crosswalk.Version {
	v := crosswalk.Version{Number: 1, Rights: crosswalk.Rights{Basis: crosswalk.RightsCustomerSupplied, Reference: "CFG-1"},
		EffectiveFrom: day.AddDate(0, -1, 0), Entries: entries, CreatedBy: maker}
	v.History = crosswalk.Open(maker, day, true)
	v.Status = crosswalk.StatusInReview
	return v
}

// WF-07 UNRESOLVED_AMBIGUITY: a code mapped twice without a discriminating
// predicate, or a partial match without one, is refused.
func TestAmbiguityIsRefused(t *testing.T) {
	partial := crosswalk.Entry{ExternalCode: "A", Description: "d", Match: crosswalk.MatchPartial, ServiceTarget: "ontology:x"}
	cases := map[string][]crosswalk.Entry{
		"partial without predicate": {partial},
		"same code twice":           {exact("A", "x"), exact("A", "y")},
	}
	for name, es := range cases {
		if err := crosswalk.CheckEntries(es); errs.ReasonOf(err) != crosswalk.ReasonUnresolvedAmbiguity {
			t.Errorf("%s: %v", name, err)
		}
	}
	a, b := exact("A", "x"), exact("A", "y")
	a.Predicate, b.Predicate = "retail", "wholesale"
	if err := crosswalk.CheckEntries([]crosswalk.Entry{a, b}); err != nil {
		t.Fatalf("predicated: %v", err)
	}
	none := crosswalk.Entry{ExternalCode: "Z", Description: "d", Match: crosswalk.MatchNoEquivalent, ServiceTarget: "ontology:x"}
	if err := crosswalk.CheckEntries([]crosswalk.Entry{none}); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("no-equivalent with a target: %v", err)
	}
}

// Four eyes, rights and revisions on approval.
func TestApprovalIsFourEyedAndRightsBound(t *testing.T) {
	v := version(exact("A", "x"))
	if _, err := v.Transition(crosswalk.StatusApproved, "ok", 2, maker, day, day); errs.ReasonOf(err) != crosswalk.ReasonMakerChecker {
		t.Fatalf("self-approval: %v", err)
	}
	if _, err := v.Transition(crosswalk.StatusApproved, "ok", 1, checker, day, day); errs.ReasonOf(err) != crosswalk.ReasonVersionConflict {
		t.Fatalf("stale: %v", err)
	}
	if _, err := v.Transition(crosswalk.StatusApproved, "", 2, checker, day, day); errs.ReasonOf(err) != errs.ReasonMissingField {
		t.Fatalf("no reason: %v", err)
	}
	lapsed := v
	lapsed.Rights.ExpiresOn = day.AddDate(0, 0, -1)
	if _, err := lapsed.Transition(crosswalk.StatusApproved, "ok", 2, checker, day, day); errs.ReasonOf(err) != crosswalk.ReasonRightsLapsed {
		t.Fatalf("lapsed: %v", err)
	}
	e, err := v.Transition(crosswalk.StatusApproved, "ok", 2, checker, day, day)
	if err != nil || e.Status != crosswalk.StatusApproved || e.Seq != 3 {
		t.Fatalf("approve: %+v %v", e, err)
	}
	folded, err := crosswalk.Fold(v, append(v.History, e))
	if err != nil || folded.Status != crosswalk.StatusApproved {
		t.Fatalf("fold: %v", err)
	}
	if _, err := folded.Transition(crosswalk.StatusInReview, "", 3, maker, day, day); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("approved back to review: %v", err)
	}
}

// Lookups are exact and advisory; nothing falls back.
func TestResolve(t *testing.T) {
	p1, p2 := exact("B", "x"), exact("B", "y")
	p1.Predicate, p2.Predicate = "retail", "wholesale"
	none := crosswalk.Entry{ExternalCode: "Z", Description: "d", Match: crosswalk.MatchNoEquivalent}
	v := version(exact("A", "x"), p1, p2, none)
	v.Status = crosswalk.StatusApproved
	for code, want := range map[string]crosswalk.Resolution{
		"A": crosswalk.ResolutionMapped, "a": crosswalk.ResolutionUnmapped, "B": crosswalk.ResolutionReviewRequired,
		"Z": crosswalk.ResolutionNoEquivalent,
	} {
		if got := v.Resolve(code, day, day); got.Resolution != want {
			t.Errorf("%s: %s, want %s", code, got.Resolution, want)
		}
	}
	if got := v.Resolve("A", day.AddDate(-1, 0, 0), day); got.Resolution != crosswalk.ResolutionNotInForce {
		t.Errorf("before window: %s", got.Resolution)
	}
	v.Status = crosswalk.StatusSuspended
	if got := v.Resolve("A", day, day); got.Resolution != crosswalk.ResolutionQuarantined {
		t.Errorf("suspended: %s", got.Resolution)
	}
}
