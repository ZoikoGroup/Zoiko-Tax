package accumulator_test

import (
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/accumulator"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

// The examples. The properties assert that nothing can go wrong in a
// particular way; these pin the specific answers at the edges the properties
// draw through only by chance.

func empty(t *testing.T) accumulator.Snapshot {
	t.Helper()
	s, err := accumulator.Empty(ref)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func contribution(t *testing.T, i int, amount string) accumulator.Contribution {
	t.Helper()
	return accumulator.Contribution{
		Key: ref.Key, SourceDecisionID: decisionID(i), Amount: fiscaltest.Money(t, amount, cur),
		EventTime: base, RecordedAt: base.Add(time.Duration(i) * time.Minute),
	}
}

func TestComparisonAtExactEquality(t *testing.T) {
	// ADR-0004 §7.1: whether a cap is crossed at equality is content, and the
	// runtime must express both answers. At exactly the limit, GTE crosses and
	// GT does not; one minor unit beyond, both do.
	for _, tc := range []struct {
		cmp           accumulator.Comparison
		atLimit, past bool
	}{
		{accumulator.AtOrAbove, true, true},
		{accumulator.Above, false, true},
	} {
		th := accumulator.Threshold{ID: "cap", Limit: fiscaltest.Money(t, "100.00", cur), Comparison: tc.cmp}

		s, crossed, err := accumulator.Apply(empty(t), contribution(t, 1, "100.00"), []accumulator.Threshold{th})
		if err != nil {
			t.Fatal(err)
		}
		if got := len(crossed) == 1; got != tc.atLimit {
			t.Errorf("%s at exactly the limit: crossed = %v, want %v", tc.cmp, got, tc.atLimit)
		}
		_, crossed, err = accumulator.Apply(s, contribution(t, 2, "0.01"), []accumulator.Threshold{th})
		if err != nil {
			t.Fatal(err)
		}
		if got := len(crossed) == 1 || s.HasCrossed("cap"); got != tc.past {
			t.Errorf("%s one minor unit past the limit: crossed = %v, want %v", tc.cmp, got, tc.past)
		}
	}
}

func TestEqualityIsByValueNotByScale(t *testing.T) {
	// 100 and 100.000 are the same amount at different precision. A limit
	// written at one scale is reached by a total at another.
	th := accumulator.Threshold{ID: "cap", Limit: fiscaltest.Money(t, "100", cur), Comparison: accumulator.AtOrAbove}
	_, crossed, err := accumulator.Apply(empty(t), contribution(t, 1, "100.000"), []accumulator.Threshold{th})
	if err != nil || len(crossed) != 1 {
		t.Fatalf("crossed %d, err %v", len(crossed), err)
	}
}

func TestComparisonHasNoDefault(t *testing.T) {
	// A threshold whose content never answered ">= or >" cannot be evaluated.
	th := accumulator.Threshold{ID: "cap", Limit: fiscaltest.Money(t, "1", cur)}
	if _, _, err := accumulator.Apply(empty(t), contribution(t, 1, "5"), []accumulator.Threshold{th}); !errs.IsCategory(err, errs.CategoryValidation) {
		t.Fatalf("a threshold with no comparison was evaluated: %v", err)
	}
}

func TestCrossingCarriesBothSidesOfTheContribution(t *testing.T) {
	// §7.1's second question — does the crossing transaction fall inside or
	// outside the threshold — needs the total before and after it.
	th := accumulator.Threshold{ID: "reg", Limit: fiscaltest.Money(t, "50.00", cur), Comparison: accumulator.Above}
	s, _, err := accumulator.Apply(empty(t), contribution(t, 1, "40.00"), []accumulator.Threshold{th})
	if err != nil {
		t.Fatal(err)
	}
	_, crossed, err := accumulator.Apply(s, contribution(t, 2, "25.00"), []accumulator.Threshold{th})
	if err != nil || len(crossed) != 1 {
		t.Fatalf("crossed %d, err %v", len(crossed), err)
	}
	c := crossed[0]
	if c.Before.String() != "40.00" || c.After.String() != "65.00" || c.Seq != 2 || c.SourceDecisionID != decisionID(2) {
		t.Fatalf("crossing %+v", c)
	}
}

func TestACreditDoesNotReEmitACrossing(t *testing.T) {
	// Up through the limit, down below it, up through it again: one crossing.
	th := []accumulator.Threshold{{ID: "cap", Limit: fiscaltest.Money(t, "10", cur), Comparison: accumulator.AtOrAbove}}
	s := empty(t)
	var total int
	for i, amount := range []string{"10", "-5", "7"} {
		next, crossed, err := accumulator.Apply(s, contribution(t, i, amount), th)
		if err != nil {
			t.Fatal(err)
		}
		total += len(crossed)
		s = next
	}
	if total != 1 || !s.HasCrossed("cap") {
		t.Fatalf("emitted %d crossings; crossed set %v", total, s.Crossed)
	}
}

func TestApplyRefusesMixedCurrencies(t *testing.T) {
	c := contribution(t, 1, "1")
	c.Amount = fiscaltest.Money(t, "1", "XXX")
	if _, _, err := accumulator.Apply(empty(t), c, nil); errs.ReasonOf(err) != errs.ReasonCurrencyMismatch {
		t.Fatalf("contribution in another currency: %v", err)
	}
	th := accumulator.Threshold{ID: "cap", Limit: fiscaltest.Money(t, "1", "XXX"), Comparison: accumulator.Above}
	if _, _, err := accumulator.Apply(empty(t), contribution(t, 1, "1"), []accumulator.Threshold{th}); errs.ReasonOf(err) != errs.ReasonCurrencyMismatch {
		t.Fatalf("threshold in another currency: %v", err)
	}
}

func TestApplyRefusesAContributionToAnotherKey(t *testing.T) {
	c := contribution(t, 1, "1")
	c.Key = "another"
	if _, _, err := accumulator.Apply(empty(t), c, nil); !errs.IsCategory(err, errs.CategoryInternal) {
		t.Fatalf("applied a contribution to the wrong key: %v", err)
	}
}

func TestApplyRefusesADuplicatedThreshold(t *testing.T) {
	th := accumulator.Threshold{ID: "cap", Limit: fiscaltest.Money(t, "1", cur), Comparison: accumulator.Above}
	if _, _, err := accumulator.Apply(empty(t), contribution(t, 1, "1"), []accumulator.Threshold{th, th}); err == nil {
		t.Fatal("a threshold declared twice was evaluated")
	}
}

func TestParseKey(t *testing.T) {
	for _, ok := range []string{"a", "tenant.jur:US-CA/tax=sales/2026-Q3", strings.Repeat("k", accumulator.MaxKeyLength), "!~"} {
		if _, err := accumulator.ParseKey(ok); err != nil {
			t.Errorf("ParseKey(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", " ", "a b", "tab\there", "line\n", "café", strings.Repeat("k", accumulator.MaxKeyLength+1)} {
		if _, err := accumulator.ParseKey(bad); err == nil {
			t.Errorf("ParseKey(%q) accepted a key the lock order cannot rely on", bad)
		}
	}
}

func TestReplayRefusesALogThatIsNotDense(t *testing.T) {
	ev := func(seq int64, i int) accumulator.Event {
		return accumulator.Event{Contribution: contribution(t, i, "1"), Seq: seq}
	}
	for name, log := range map[string][]accumulator.Event{
		"gap":                {ev(1, 1), ev(3, 2)},
		"starts above one":   {ev(2, 1)},
		"out of order":       {ev(2, 1), ev(1, 2)},
		"decision twice":     {ev(1, 1), ev(2, 1)},
		"repeated sequence":  {ev(1, 1), ev(1, 2)},
		"zero is not a seq":  {ev(0, 1)},
		"negative sequences": {ev(-1, 1)},
	} {
		if _, _, err := accumulator.Replay(ref, log, nil); !errs.IsCategory(err, errs.CategoryInternal) {
			t.Errorf("%s: replay accepted the log (%v)", name, err)
		}
		if _, err := accumulator.Rebuild(ref, log, nil); !errs.IsCategory(err, errs.CategoryInternal) {
			t.Errorf("%s: rebuild accepted the log (%v)", name, err)
		}
	}
}

func TestRebuildRefusesCrossingsTheLogCannotExplain(t *testing.T) {
	log := []accumulator.Event{{Contribution: contribution(t, 1, "5"), Seq: 1}}
	th := accumulator.Threshold{ID: "cap", Limit: fiscaltest.Money(t, "1", cur), Comparison: accumulator.Above}
	for name, cs := range map[string][]accumulator.Crossing{
		"beyond the log": {{Key: ref.Key, Threshold: th, Seq: 2}},
		"another key":    {{Key: "other", Threshold: th, Seq: 1}},
		"crossed twice":  {{Key: ref.Key, Threshold: th, Seq: 1}, {Key: ref.Key, Threshold: th, Seq: 1}},
	} {
		if _, err := accumulator.Rebuild(ref, log, cs); err == nil {
			t.Errorf("%s: rebuild accepted the crossings", name)
		}
	}
}

func TestEmptySnapshot(t *testing.T) {
	s := empty(t)
	if s.LastSeq != 0 || !s.Total.IsZero() || !s.UpdatedAt.IsZero() || len(s.Crossed) != 0 {
		t.Fatalf("empty snapshot %+v", s)
	}
	rebuilt, err := accumulator.Rebuild(ref, nil, nil)
	if err != nil || rebuilt.Total.String() != s.Total.String() || rebuilt.LastSeq != 0 {
		t.Fatalf("rebuilding an empty log: %+v, %v", rebuilt, err)
	}
	if (accumulator.Observation{}).Authoritative() {
		t.Fatal("a quote observation claims to be authoritative")
	}
}
