package app_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

func listedRecord(n int, key string, at time.Time, outcome evidence.Outcome, supersedes *id.DecisionID) evidence.Record {
	u := uuid.MustParse("01920000-0000-7000-8000-00000000d000")
	u[15] = byte(n)
	return evidence.Record{
		DecisionID: id.NewDecisionID(u), TenantID: tenantID, BusinessKey: key, Supersedes: supersedes,
		ValidFrom: at, RecordedAt: at, EventTime: at, Outcome: outcome,
	}
}

func TestDecisionsListLatestFirstWithSuccessor(t *testing.T) {
	h := newHarness(t)
	t0 := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	first := listedRecord(1, "INV-1", t0, evidence.OutcomeAdvisory, nil)
	other := listedRecord(2, "INV-2", t0.Add(time.Minute), evidence.OutcomeUnsupported, nil)
	correction := listedRecord(3, "INV-1", t0.Add(2*time.Minute), evidence.OutcomeAdvisory, &first.DecisionID)
	for _, r := range []evidence.Record{first, other, correction} {
		if err := h.decisions.Append(as(security.RoleOperator), r); err != nil {
			t.Fatal(err)
		}
	}

	all, err := h.svc.Decisions(as(security.RoleAuditor), port.DecisionFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Record.DecisionID != correction.DecisionID || all[2].Record.DecisionID != first.DecisionID {
		t.Fatalf("want latest first, got %+v", all)
	}
	if all[2].SupersededBy == nil || *all[2].SupersededBy != correction.DecisionID || all[0].SupersededBy != nil {
		t.Fatalf("successor not reported: %+v", all)
	}

	byKey, err := h.svc.Decisions(as(security.RoleAnalyst), port.DecisionFilter{BusinessKey: "INV-1"})
	if err != nil || len(byKey) != 2 {
		t.Fatalf("business key filter: %v %+v", err, byKey)
	}
	byOutcome, err := h.svc.Decisions(as(security.RoleOperator), port.DecisionFilter{Outcome: evidence.OutcomeUnsupported})
	if err != nil || len(byOutcome) != 1 || byOutcome[0].Record.DecisionID != other.DecisionID {
		t.Fatalf("outcome filter: %v %+v", err, byOutcome)
	}
}

func TestDecisionsListRefusals(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.Decisions(as(security.RoleOperator), port.DecisionFilter{Outcome: "MAYBE"}); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("unknown outcome: want %s, got %v", errs.ReasonInvalidValue, err)
	}
	// ADMIN administers users and is on no fiscal read.
	if _, err := h.svc.Decisions(as(security.RoleAdmin), port.DecisionFilter{}); err == nil {
		t.Fatal("an administrator listed decisions")
	}
}
