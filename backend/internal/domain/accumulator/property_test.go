package accumulator_test

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"pgregory.net/rapid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/accumulator"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// Tier 1 of ADR-0018 §2.1, on the invariants ADR-0004 §5.1 control 3 names:
// contribution idempotence, crossing exactly-once, total monotonicity, and
// order-independence of same-period contributions — plus §2.5's "the snapshot
// is provably redundant", and §2.3's lock order.
//
// What these can and cannot prove is worth being exact about. Serialization
// and the UNIQUE constraint are the database's, and only a test against
// PostgreSQL proves those (accumulator_integration_test.go in the adapter).
// What is proved here is that the arithmetic the serialized commits perform
// composes correctly: given commits that arrive one at a time, in any order,
// with any retries, the totals and crossings are the ones the ADR promises.
//
// rapid runs from a fixed seed unless one is passed on the command line, so a
// failure here reproduces (ADR-0018 §2.12).

const cur fiscal.Currency = "XTS"

var (
	ref  = accumulator.Ref{Key: "tenant.jur.tax.2026-Q3", Currency: cur}
	base = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
)

// ---------------------------------------------------------------------------
// generators
// ---------------------------------------------------------------------------

// decimal renders n * 10^-scale in the canonical plain form.
func decimal(n int64, scale int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	digits := strconv.FormatInt(n, 10)
	if scale > 0 {
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		digits = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	}
	if neg {
		digits = "-" + digits
	}
	return digits
}

func money(t *rapid.T, amount string) fiscal.Money {
	m, err := fiscal.ParseMoney(amount, cur)
	if err != nil {
		t.Fatalf("parse %q: %v", amount, err)
	}
	return m
}

// drawAmount draws an amount at a scale of 0 to 3 — mixed scales on purpose,
// because exact addition of 1.5 and 0.25 must not depend on which came first.
func drawAmount(t *rapid.T, label string, nonNegative bool) fiscal.Money {
	lo := int64(-500000)
	if nonNegative {
		lo = 0
	}
	n := rapid.Int64Range(lo, 500000).Draw(t, label)
	scale := rapid.IntRange(0, 3).Draw(t, label+".scale")
	return money(t, decimal(n, scale))
}

func decisionID(i int) id.DecisionID {
	var u uuid.UUID
	u[0], u[1], u[2], u[15] = 0xac, byte(i>>8), byte(i), 0x01
	return id.NewDecisionID(u)
}

func drawContributions(t *rapid.T, nonNegative bool) []accumulator.Contribution {
	n := rapid.IntRange(1, 25).Draw(t, "contributions")
	out := make([]accumulator.Contribution, n)
	for i := range out {
		out[i] = accumulator.Contribution{
			Key:              ref.Key,
			SourceDecisionID: decisionID(i),
			Amount:           drawAmount(t, fmt.Sprintf("amount[%d]", i), nonNegative),
			EventTime:        base.Add(time.Duration(i) * time.Hour),
			RecordedAt:       base.Add(time.Duration(i)*time.Hour + time.Minute),
		}
	}
	return out
}

func drawThresholds(t *rapid.T) []accumulator.Threshold {
	ids := rapid.SliceOfDistinct(rapid.SampledFrom([]string{"cap", "de-minimis", "reg.trigger", "t3", "t4"}),
		func(s string) string { return s }).Draw(t, "threshold ids")
	out := make([]accumulator.Threshold, len(ids))
	for i, tid := range ids {
		out[i] = accumulator.Threshold{
			ID:         accumulator.ThresholdID(tid),
			Limit:      drawAmount(t, "limit."+tid, true),
			Comparison: rapid.SampledFrom([]accumulator.Comparison{accumulator.AtOrAbove, accumulator.Above}).Draw(t, "cmp."+tid),
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// a model of the commit path
// ---------------------------------------------------------------------------

// ledger is what the database holds for one key: the log, the snapshot, and
// the recorded crossings. commit performs one ADR-0004 §2.2 transaction with
// the lock already held, and models the UNIQUE (key, source_decision_id)
// constraint exactly as the adapter reports it — a second contribution from
// one decision is "already applied", writes nothing, and changes nothing.
type ledger struct {
	snap      accumulator.Snapshot
	events    []accumulator.Event
	crossings []accumulator.Crossing
	applied   map[id.DecisionID]bool
	// steps records the total after each applied contribution.
	steps []fiscal.Money
}

func newLedger(t *rapid.T) *ledger {
	s, err := accumulator.Empty(ref)
	if err != nil {
		t.Fatal(err)
	}
	return &ledger{snap: s, applied: map[id.DecisionID]bool{}}
}

func (l *ledger) commit(t *rapid.T, c accumulator.Contribution, thresholds []accumulator.Threshold) bool {
	next, crossed, err := accumulator.Apply(l.snap, c, thresholds)
	if err != nil {
		t.Fatalf("apply %s: %v", c.Amount, err)
	}
	if l.applied[c.SourceDecisionID] {
		// The insert is refused before the snapshot is saved; the transaction
		// returns the original result and writes nothing else.
		return false
	}
	l.applied[c.SourceDecisionID] = true
	l.events = append(l.events, accumulator.Event{Contribution: c, Seq: next.LastSeq})
	l.crossings = append(l.crossings, crossed...)
	l.snap = next
	l.steps = append(l.steps, next.Total)
	return true
}

func run(t *rapid.T, cs []accumulator.Contribution, thresholds []accumulator.Threshold) *ledger {
	l := newLedger(t)
	for _, c := range cs {
		l.commit(t, c, thresholds)
	}
	return l
}

func sameSnapshot(a, b accumulator.Snapshot) bool {
	return a.Key == b.Key &&
		a.Total.Currency() == b.Total.Currency() && a.Total.String() == b.Total.String() &&
		a.LastSeq == b.LastSeq &&
		slices.Equal(a.Crossed, b.Crossed) &&
		a.UpdatedAt.Equal(b.UpdatedAt)
}

func describe(s accumulator.Snapshot) string {
	return fmt.Sprintf("{%s total=%s seq=%d crossed=%v at=%s}", s.Key, s.Total, s.LastSeq, s.Crossed, s.UpdatedAt.Format(time.RFC3339))
}

func crossingIDs(cs []accumulator.Crossing) []accumulator.ThresholdID {
	out := make([]accumulator.ThresholdID, len(cs))
	for i, c := range cs {
		out[i] = c.Threshold.ID
	}
	return out
}

func cmp(t *rapid.T, a, b fiscal.Money) int {
	d, err := a.Sub(b)
	if err != nil {
		t.Fatal(err)
	}
	if d.IsZero() {
		return 0
	}
	p, err := d.NumericParts()
	if err != nil {
		t.Fatal(err)
	}
	if p.Negative {
		return -1
	}
	return 1
}

// ---------------------------------------------------------------------------
// properties
// ---------------------------------------------------------------------------

func TestApplyIsPure(t *testing.T) {
	// The commit path and Replay both call Apply; if it depended on anything
	// but its arguments, or reached back into the snapshot it was given, the
	// two would disagree in exactly the case the rebuild exists for.
	rapid.Check(t, func(rt *rapid.T) {
		thresholds := drawThresholds(rt)
		l := run(rt, drawContributions(rt, false), thresholds)
		before := l.snap
		crossedBefore := slices.Clone(before.Crossed)
		c := accumulator.Contribution{
			Key: ref.Key, SourceDecisionID: decisionID(9999), Amount: drawAmount(rt, "next", false),
			EventTime: base, RecordedAt: base.Add(1000 * time.Hour),
		}

		first, firstCrossed, err := accumulator.Apply(before, c, thresholds)
		if err != nil {
			rt.Fatal(err)
		}
		second, secondCrossed, err := accumulator.Apply(before, c, thresholds)
		if err != nil {
			rt.Fatal(err)
		}
		if !sameSnapshot(first, second) || !slices.Equal(crossingIDs(firstCrossed), crossingIDs(secondCrossed)) {
			rt.Fatalf("Apply is not deterministic: %s vs %s", describe(first), describe(second))
		}
		if !slices.Equal(before.Crossed, crossedBefore) {
			rt.Fatalf("Apply mutated its input's crossed set: %v, was %v", before.Crossed, crossedBefore)
		}
		if len(first.Crossed) > 0 && len(before.Crossed) > 0 && &first.Crossed[0] == &before.Crossed[0] {
			rt.Fatalf("Apply's result aliases its input's crossed set")
		}
	})
}

func TestRetriedContributionsCountOnce(t *testing.T) {
	// ADR-0004 §2.4 and control 3 (contribution idempotence): a retried commit
	// must not count twice. Retries are interleaved anywhere — immediately, or
	// long after other commits — and the outcome must be the outcome of the
	// distinct contributions committed once each, in first-arrival order.
	rapid.Check(t, func(rt *rapid.T) {
		thresholds := drawThresholds(rt)
		distinct := drawContributions(rt, false)

		var arrivals []accumulator.Contribution
		for i, c := range distinct {
			arrivals = append(arrivals, c)
			for r := rapid.IntRange(0, 2).Draw(rt, fmt.Sprintf("retries[%d]", i)); r > 0; r-- {
				// A retry of this one or of any earlier one.
				j := rapid.IntRange(0, i).Draw(rt, fmt.Sprintf("retry[%d].of", i))
				arrivals = append(arrivals, distinct[j])
			}
		}

		once := run(rt, distinct, thresholds)
		retried := run(rt, arrivals, thresholds)

		if !sameSnapshot(once.snap, retried.snap) {
			rt.Fatalf("retries changed the outcome: once %s, with retries %s", describe(once.snap), describe(retried.snap))
		}
		if !slices.Equal(crossingIDs(once.crossings), crossingIDs(retried.crossings)) {
			rt.Fatalf("retries changed the crossings: %v vs %v", crossingIDs(once.crossings), crossingIDs(retried.crossings))
		}
		if len(retried.events) != len(distinct) {
			rt.Fatalf("%d distinct decisions produced %d log entries", len(distinct), len(retried.events))
		}
	})
}

func TestEachThresholdCrossesExactlyOnce(t *testing.T) {
	// ADR-0004 §2.6 and control 3 (crossing exactly-once), over signed
	// contributions — credits included, so totals that rise through a limit,
	// fall back below it and rise through it again.
	//
	// Three things must hold: no threshold is emitted twice; a threshold is
	// emitted at the first commit whose new total reaches it, and by that
	// commit — "no path where the total advances past a threshold without the
	// event"; and the snapshot's crossed set is exactly what was emitted.
	rapid.Check(t, func(rt *rapid.T) {
		thresholds := drawThresholds(rt)
		nonNegative := rapid.Bool().Draw(rt, "non-negative")
		led := run(rt, drawContributions(rt, nonNegative), thresholds)
		emitted := crossingIDs(led.crossings)
		sorted := slices.Clone(emitted)
		slices.Sort(sorted)
		if len(slices.Compact(slices.Clone(sorted))) != len(sorted) {
			rt.Fatalf("a threshold was emitted twice: %v", emitted)
		}
		if !slices.Equal(sorted, led.snap.Crossed) {
			rt.Fatalf("emitted %v but the snapshot records %v", sorted, led.snap.Crossed)
		}
		for _, th := range thresholds {
			wantSeq := int64(0)
			for i, total := range led.steps {
				reached, err := th.Reached(total)
				if err != nil {
					rt.Fatal(err)
				}
				if reached {
					wantSeq = int64(i + 1)
					break
				}
			}
			var got []accumulator.Crossing
			for _, c := range led.crossings {
				if c.Threshold.ID == th.ID {
					got = append(got, c)
				}
			}
			switch {
			case wantSeq == 0 && len(got) != 0:
				rt.Fatalf("%s was never reached but was emitted at seq %d", th.ID, got[0].Seq)
			case wantSeq != 0 && len(got) != 1:
				rt.Fatalf("%s was first reached at seq %d but emitted %d times", th.ID, wantSeq, len(got))
			case wantSeq != 0 && got[0].Seq != wantSeq:
				rt.Fatalf("%s was first reached at seq %d but attributed to seq %d", th.ID, wantSeq, got[0].Seq)
			case wantSeq != 0:
				c := got[0]
				ev := led.events[c.Seq-1]
				if c.SourceDecisionID != ev.SourceDecisionID || c.After.String() != led.steps[c.Seq-1].String() {
					rt.Fatalf("%s's crossing does not name the contribution that crossed it", th.ID)
				}
				if after, err := c.Before.Add(ev.Amount); err != nil || after.String() != c.After.String() {
					rt.Fatalf("%s's crossing: before %s + %s is not after %s", th.ID, c.Before, ev.Amount, c.After)
				}
			}
		}
	})
}

func TestTotalIsMonotonicForNonNegativeContributions(t *testing.T) {
	// Control 3 (total monotonicity). With no credits the total never falls,
	// so a threshold once reached stays reached — which is what makes "crossed
	// once" and "currently at or above" the same statement in that case.
	rapid.Check(t, func(rt *rapid.T) {
		l := run(rt, drawContributions(rt, true), drawThresholds(rt))
		zero := money(rt, "0")
		prev := zero
		for i, total := range l.steps {
			if cmp(rt, total, prev) < 0 {
				rt.Fatalf("total fell from %s to %s at seq %d with no negative contribution", prev, total, i+1)
			}
			prev = total
		}
	})
}

func TestSamePeriodContributionsAreOrderIndependent(t *testing.T) {
	// Control 3 (order-independence). Concurrent commits on one key are
	// serialized in whatever order their locks happen to be granted. That
	// order must not change the answer: the final total, for any signs; and,
	// with no credits, the set of thresholds crossed.
	//
	// Which commit is credited with a crossing does depend on order — the one
	// that took the total over the line — and that is correct rather than a
	// weakness: it is the fact an audit asks for. With credits, even the set
	// can depend on order (a peak reached in one order and not another), which
	// is why whether a credit can "un-cross" anything is content, not
	// arithmetic.
	rapid.Check(t, func(rt *rapid.T) {
		thresholds := drawThresholds(rt)
		nonNegative := rapid.Bool().Draw(rt, "non-negative")
		cs := drawContributions(rt, nonNegative)
		shuffled := rapid.Permutation(cs).Draw(rt, "arrival order")

		a, b := run(rt, cs, thresholds), run(rt, shuffled, thresholds)
		if a.snap.Total.String() != b.snap.Total.String() || a.snap.LastSeq != b.snap.LastSeq {
			rt.Fatalf("arrival order changed the total: %s vs %s", describe(a.snap), describe(b.snap))
		}
		if nonNegative && !slices.Equal(a.snap.Crossed, b.snap.Crossed) {
			rt.Fatalf("arrival order changed which thresholds crossed: %v vs %v", a.snap.Crossed, b.snap.Crossed)
		}
	})
}

func TestRebuildEqualsIncrementalApply(t *testing.T) {
	// ADR-0004 §2.5: the snapshot is provably redundant. Rebuilding from the
	// log and the recorded crossings reproduces the snapshot that incremental
	// commits produced, exactly — total, scale, sequence, crossed set and
	// time — and replaying the log under the same thresholds reproduces the
	// crossings, in order.
	rapid.Check(t, func(rt *rapid.T) {
		thresholds := drawThresholds(rt)
		l := run(rt, drawContributions(rt, false), thresholds)

		rebuilt, err := accumulator.Rebuild(ref, l.events, l.crossings)
		if err != nil {
			rt.Fatal(err)
		}
		if !sameSnapshot(rebuilt, l.snap) {
			rt.Fatalf("rebuild %s differs from incremental %s", describe(rebuilt), describe(l.snap))
		}

		replayed, crossings, err := accumulator.Replay(ref, l.events, thresholds)
		if err != nil {
			rt.Fatal(err)
		}
		if !sameSnapshot(replayed, l.snap) {
			rt.Fatalf("replay %s differs from incremental %s", describe(replayed), describe(l.snap))
		}
		if len(crossings) != len(l.crossings) {
			rt.Fatalf("replay emitted %d crossings, the commits %d", len(crossings), len(l.crossings))
		}
		for i := range crossings {
			x, y := crossings[i], l.crossings[i]
			if x.Threshold.ID != y.Threshold.ID || x.Seq != y.Seq || x.SourceDecisionID != y.SourceDecisionID ||
				x.Before.String() != y.Before.String() || x.After.String() != y.After.String() {
				rt.Fatalf("replay crossing %d differs: %+v vs %+v", i, x, y)
			}
		}
	})
}

func TestLockOrderIsSortedDistinctAndIdempotent(t *testing.T) {
	// ADR-0004 §2.3 and control 1. Deadlock freedom rests on every commit
	// taking its locks in one total order; that is only true if LockOrder is
	// a function of the set of keys and nothing else.
	keyGen := rapid.SampledFrom([]accumulator.Key{"a", "b", "B", "a.1", "a-1", "z", "tenant/x", "~", "!"})
	rapid.Check(t, func(rt *rapid.T) {
		keys := rapid.SliceOf(keyGen).Draw(rt, "keys")
		input := slices.Clone(keys)

		got := accumulator.LockOrder(keys)
		if !slices.Equal(keys, input) {
			rt.Fatalf("LockOrder modified its input")
		}
		for i := 1; i < len(got); i++ {
			if got[i-1] >= got[i] {
				rt.Fatalf("not strictly ascending: %v", got)
			}
		}
		for _, k := range keys {
			if !slices.Contains(got, k) {
				rt.Fatalf("%s was dropped: %v from %v", k, got, keys)
			}
		}
		if again := accumulator.LockOrder(got); !slices.Equal(again, got) {
			rt.Fatalf("not idempotent: %v then %v", got, again)
		}
		if shuffled := accumulator.LockOrder(rapid.Permutation(keys).Draw(rt, "shuffled")); !slices.Equal(shuffled, got) {
			rt.Fatalf("depends on input order: %v vs %v", shuffled, got)
		}
	})
}
