package retention_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/retention"
)

var (
	entity  = id.NewLegalEntityID(uuid.MustParse("00000000-0000-7000-8000-00000000e001"))
	other   = id.NewLegalEntityID(uuid.MustParse("00000000-0000-7000-8000-00000000e002"))
	dec     = id.NewDecisionID(uuid.MustParse("00000000-0000-7000-8000-00000000d001"))
	event   = time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC)
	holdOne = id.NewLegalHoldID(uuid.MustParse("00000000-0000-7000-8000-00000000a001"))
)

func record() retention.Record {
	return retention.Record{Class: retention.ClassDecision, Ref: dec.String(), LegalEntity: entity, Country: "DE",
		BusinessKey: "INV-1/1", Decision: dec, EventTime: event, RecordedAt: event.Add(time.Hour)}
}

func policy(id string, version, years int, from time.Time) retention.Policy {
	return retention.Policy{ID: id, Version: version, Class: retention.ClassDecision, Country: "DE", Years: years,
		Trigger: retention.TriggerYearEnd, EffectiveFrom: from, Citation: "AO §147"}
}

func held(scope retention.Scope, released bool) retention.Hold {
	h := retention.Hold{ID: holdOne, Status: retention.HoldActive, Scope: scope}
	if released {
		h.Status = retention.HoldReleased
	}
	return h
}

// ZTAX-EVID-REQ-0022, -0092, -0126: no global period; a record no policy
// governs, or two disagreeing policies govern, is kept and escalated; the
// verdict names the version it used.
func TestEVIDREQ0022RetentionIsResolvedByPolicyAndFailsSafe(t *testing.T) {
	since := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	ten := policy("de-decisions", 1, 10, since)

	// Ten years from the end of 2026: retained until 2037-01-01.
	v := retention.Evaluate(record(), []retention.Policy{ten}, nil, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	if v.Outcome != retention.OutcomeRetain || !v.RetainUntil.Equal(time.Date(2037, 1, 1, 0, 0, 0, 0, time.UTC)) ||
		v.Policy == nil || v.Policy.Version != 1 {
		t.Fatalf("inside the period: %+v", v)
	}
	if v := retention.Evaluate(record(), []retention.Policy{ten}, nil, time.Date(2037, 1, 1, 0, 0, 0, 0, time.UTC)); v.Outcome != retention.OutcomeEligible {
		t.Fatalf("at the end of the period: %s", v.Outcome)
	}

	// A later version governs from its effective date, and only from then.
	eleven := policy("de-decisions", 2, 11, time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC))
	if v := retention.Evaluate(record(), []retention.Policy{ten, eleven}, nil, time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)); v.Policy.Version != 1 {
		t.Fatalf("before version 2 is effective: version %d", v.Policy.Version)
	}
	if v := retention.Evaluate(record(), []retention.Policy{ten, eleven}, nil, time.Date(2037, 6, 1, 0, 0, 0, 0, time.UTC)); v.Outcome != retention.OutcomeRetain || v.Policy.Version != 2 {
		t.Fatalf("under version 2: %s v%d", v.Outcome, v.Policy.Version)
	}

	// No policy for the country, no country at all, or two that disagree:
	// kept, never guessed.
	fr := record()
	fr.Country = "FR"
	nowhere := record()
	nowhere.Country = ""
	for name, r := range map[string]retention.Record{"another country": fr, "no country": nowhere} {
		if v := retention.Evaluate(r, []retention.Policy{ten}, nil, time.Date(2090, 1, 1, 0, 0, 0, 0, time.UTC)); v.Outcome != retention.OutcomeNoPolicy || v.Outcome.Disposable() {
			t.Errorf("%s: %s", name, v.Outcome)
		}
	}
	six := policy("de-commercial", 1, 6, since)
	if v := retention.Evaluate(record(), []retention.Policy{ten, six}, nil, time.Date(2090, 1, 1, 0, 0, 0, 0, time.UTC)); v.Outcome != retention.OutcomeConflicted || v.Outcome.Disposable() || len(v.Candidates) != 2 {
		t.Fatalf("two disagreeing policies: %+v", v)
	}
	if err := (retention.Policy{ID: "x", Version: 1, Class: retention.ClassDecision, Country: "DE", Years: 10,
		Trigger: retention.TriggerEventTime, EffectiveFrom: since}).Validate(); err == nil {
		t.Fatal("a policy citing no authority validated")
	}
}

// ZTAX-EVID-REQ-0023, ZTAX-PRIV-REQ-0050: an active hold blocks disposition
// of what it scopes, and only that.
func TestEVIDREQ0023AHoldBlocksDispositionOfWhatItScopesOnly(t *testing.T) {
	ten := policy("de-decisions", 1, 10, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	late := time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC)
	byKey := retention.Scope{BusinessKeys: []string{"INV-1/1"}}
	if v := retention.Evaluate(record(), []retention.Policy{ten}, []retention.Hold{held(byKey, false)}, late); v.Outcome != retention.OutcomeHeld || len(v.Holds) != 1 {
		t.Fatalf("held past its period: %+v", v)
	}
	if v := retention.Evaluate(record(), []retention.Policy{ten}, []retention.Hold{held(byKey, true)}, late); v.Outcome != retention.OutcomeEligible {
		t.Fatalf("after release, retention decides again (ZTAX-EVID-REQ-0054): %s", v.Outcome)
	}
	for name, scope := range map[string]retention.Scope{
		"another key":               {BusinessKeys: []string{"INV-2/1"}},
		"another legal entity":      {LegalEntity: other, Decisions: []id.DecisionID{dec}},
		"a window that ends before": {EventFrom: event.Add(-48 * time.Hour), EventTo: event},
	} {
		if v := retention.Evaluate(record(), []retention.Policy{ten}, []retention.Hold{held(scope, false)}, late); v.Outcome != retention.OutcomeEligible {
			t.Errorf("%s held the record: %s", name, v.Outcome)
		}
	}
	window := retention.Scope{LegalEntity: entity, EventFrom: event.Add(-time.Hour), EventTo: event.Add(time.Hour)}
	if v := retention.Evaluate(record(), []retention.Policy{ten}, []retention.Hold{held(window, false)}, late); v.Outcome != retention.OutcomeHeld {
		t.Fatalf("a window covering the event: %s", v.Outcome)
	}
}

// ZTAX-PRIV-REQ-0051: a hold scope is specific — never a tenant at large.
func TestPRIVREQ0051AHoldScopeIsSpecific(t *testing.T) {
	for name, s := range map[string]retention.Scope{
		"nothing":               {},
		"a legal entity alone":  {LegalEntity: entity},
		"an open window":        {EventFrom: event},
		"a backwards window":    {EventFrom: event, EventTo: event.Add(-time.Hour)},
		"a twenty-year window":  {EventFrom: event, EventTo: event.AddDate(20, 0, 0)},
		"an empty business key": {BusinessKeys: []string{""}},
	} {
		if err := s.Validate(); err == nil {
			t.Errorf("%s validated", name)
		}
	}
	if err := (retention.Scope{Decisions: []id.DecisionID{dec}}).Validate(); err != nil {
		t.Fatal(err)
	}
}

// ZTAX-EVID-REQ-0053: a hold's scope changes are a history, and a release is
// final.
func TestEVIDREQ0053HoldHistoryFolds(t *testing.T) {
	a := retention.Scope{Decisions: []id.DecisionID{dec}}
	b := retention.Scope{BusinessKeys: []string{"INV-1/1", "INV-2/1"}}
	events := []retention.HoldEvent{
		{Seq: 1, Kind: retention.EventPlaced, Scope: a},
		{Seq: 2, Kind: retention.EventScopeChanged, Scope: b},
		{Seq: 3, Kind: retention.EventReleased},
	}
	h, err := retention.Fold(holdOne, id.TenantID{}, "matter-1", events)
	if err != nil || h.Active() || len(h.Scope.BusinessKeys) != 2 {
		t.Fatalf("folded %+v %v", h, err)
	}
	if _, err := retention.Fold(holdOne, id.TenantID{}, "m", append(events, retention.HoldEvent{Seq: 4, Kind: retention.EventScopeChanged, Scope: a})); err == nil {
		t.Fatal("a released hold changed scope")
	}
	if _, err := retention.Fold(holdOne, id.TenantID{}, "m", []retention.HoldEvent{{Seq: 1, Kind: retention.EventReleased}}); err == nil {
		t.Fatal("a hold released before it was placed")
	}
}
