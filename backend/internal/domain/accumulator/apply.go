package accumulator

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// Apply folds one contribution into a snapshot and reports the thresholds the
// contribution crossed.
//
// It is the step inside the ADR-0004 §2.2 transaction that says "evaluate
// thresholds against the new total, in Go": the caller has locked the
// snapshot row, calls Apply, and then writes what it returns — the
// contribution at newSnapshot.LastSeq, the new snapshot, one threshold_crossing
// row and one outbox row per crossing — before committing. Apply itself
// changes nothing; s is not modified, and the returned snapshot shares no
// mutable state with it.
//
// Exactly-once crossing (ADR-0004 §2.6). A threshold is crossed when the new
// total reaches it and it is not already in s.Crossed. Once crossed it stays
// crossed: a later credit that takes the total back below the limit does not
// remove it from the set, and a later contribution that reaches the limit
// again does not emit it again. Whether a credit "un-crosses" a registration
// trigger is a legal question (OBL-001, DET-001), and answering it by
// re-emitting would be answering it silently, in the arithmetic, and
// differently depending on the order commits happened to arrive in. The
// accumulator records that the boundary was crossed and by which
// contribution; what that means is decided by whoever consumes the event.
//
// A threshold that is already reached when it is first supplied — new content
// introducing a limit the total is already beyond — is crossed by the next
// contribution, and the crossing names that contribution. That is the honest
// attribution: it is the first commit evaluated against the threshold.
//
// Crossings are returned in ascending threshold-id order, so the same inputs
// produce the same rows and the same outbox events in the same order, which
// is what lets a replay compare them.
func Apply(s Snapshot, c Contribution, thresholds []Threshold) (Snapshot, []Crossing, error) {
	if err := s.Validate(); err != nil {
		return Snapshot{}, nil, err
	}
	if err := c.Validate(); err != nil {
		return Snapshot{}, nil, err
	}
	if c.Key != s.Key {
		// The caller locked one key and is applying a contribution to another.
		// That is a wiring defect, and writing either row would be wrong.
		return Snapshot{}, nil, errs.New(errs.CategoryInternal, errs.ReasonInternal,
			fmt.Sprintf("A contribution to %s was applied to the snapshot of %s.", c.Key, s.Key))
	}
	currency := s.Total.Currency()
	if c.Amount.Currency() != currency {
		return Snapshot{}, nil, currencyMismatch(currency, c.Amount.Currency())
	}
	if s.LastSeq == math.MaxInt64 {
		return Snapshot{}, nil, integrity(s.Key, "has exhausted its sequence")
	}
	ordered, err := checkThresholds(s.Ref(), thresholds)
	if err != nil {
		return Snapshot{}, nil, err
	}

	after, err := s.Total.Add(c.Amount)
	if err != nil {
		return Snapshot{}, nil, err
	}
	next := Snapshot{
		Key:       s.Key,
		Total:     after,
		LastSeq:   s.LastSeq + 1,
		Crossed:   slices.Clone(s.Crossed),
		UpdatedAt: c.RecordedAt,
	}
	if next.Crossed == nil {
		next.Crossed = []ThresholdID{}
	}

	var crossings []Crossing
	for _, t := range ordered {
		if s.HasCrossed(t.ID) {
			continue
		}
		reached, err := t.Reached(after)
		if err != nil {
			return Snapshot{}, nil, err
		}
		if !reached {
			continue
		}
		crossings = append(crossings, Crossing{
			Key:              s.Key,
			Threshold:        t,
			Seq:              next.LastSeq,
			SourceDecisionID: c.SourceDecisionID,
			Before:           s.Total,
			After:            after,
			RecordedAt:       c.RecordedAt,
		})
		// ordered is ascending by id, so appending keeps Crossed sorted only if
		// every new id sorts after every old one — which it need not. Insert.
		i, _ := slices.BinarySearch(next.Crossed, t.ID)
		next.Crossed = slices.Insert(next.Crossed, i, t.ID)
	}
	return next, crossings, nil
}

// checkThresholds validates a threshold set against the accumulator it is
// evaluated on and returns it sorted by id.
func checkThresholds(ref Ref, thresholds []Threshold) ([]Threshold, error) {
	ordered := slices.Clone(thresholds)
	slices.SortFunc(ordered, func(a, b Threshold) int { return strings.Compare(string(a.ID), string(b.ID)) })
	for i, t := range ordered {
		if err := t.Validate(); err != nil {
			return nil, err
		}
		if t.Limit.Currency() != ref.Currency {
			return nil, currencyMismatch(ref.Currency, t.Limit.Currency())
		}
		if i > 0 && ordered[i-1].ID == t.ID {
			// Two definitions of one threshold would make "crossed" depend on
			// which one was looked at. Content that declares this is defective.
			return nil, errs.Invalid("thresholds", errs.ReasonInvalidValue,
				fmt.Sprintf("Threshold %s is declared more than once for accumulator %s.", t.ID, ref.Key))
		}
	}
	return ordered, nil
}

// Replay recomputes an accumulator from its log under a threshold set: the
// snapshot the commits would have produced, and every crossing they would have
// emitted, in order.
//
// It is a fold of Apply from Empty, so it cannot disagree with the commit path
// by construction — there is no second implementation of the arithmetic to
// drift. The reconciliation job of ADR-0004 §5.1 control 2 compares its
// result against the stored snapshot and the recorded crossings.
//
// The crossings are recomputed under the thresholds given, not under the
// thresholds that were in force when each contribution was committed. Where
// content changed a limit mid-log they can legitimately differ from what was
// recorded, and the recorded crossings — which were emitted and are evidence —
// are the truth. Rebuild is the snapshot recovery path for that reason; Replay
// is the check.
//
// events must be the whole log for the key, in sequence order. A gap, a
// reordering or a repeated source decision is reported as an integrity
// failure rather than repaired: the constraints of migration 000003 and 000007
// make each impossible, so meeting one means the log itself is not what was
// written.
func Replay(ref Ref, events []Event, thresholds []Threshold) (Snapshot, []Crossing, error) {
	s, err := Empty(ref)
	if err != nil {
		return Snapshot{}, nil, err
	}
	seen := make(map[id.DecisionID]int64, len(events))
	var all []Crossing
	for _, e := range events {
		if e.Seq != s.LastSeq+1 {
			return Snapshot{}, nil, integrity(ref.Key,
				fmt.Sprintf("log has seq %d where %d was expected; it is not dense and ordered", e.Seq, s.LastSeq+1))
		}
		if prior, dup := seen[e.SourceDecisionID]; dup {
			return Snapshot{}, nil, integrity(ref.Key,
				fmt.Sprintf("log records decision %s at seq %d and again at seq %d", e.SourceDecisionID, prior, e.Seq))
		}
		seen[e.SourceDecisionID] = e.Seq
		next, crossed, err := Apply(s, e.Contribution, thresholds)
		if err != nil {
			return Snapshot{}, nil, err
		}
		s = next
		all = append(all, crossed...)
	}
	return s, all, nil
}

// Rebuild recomputes a snapshot from the two append-only logs it summarises:
// the contributions, for the total, and the recorded crossings, for the set of
// thresholds already emitted (ADR-0004 §2.5).
//
// This is what makes snapshot corruption an availability incident rather than
// a fiscal one. The snapshot holds nothing the logs do not; Rebuild is the
// proof, run as an operation, and the property tests assert it equals the
// snapshot incremental commits produced.
//
// The crossed set comes from the recorded crossings rather than from
// re-evaluating thresholds, because a crossing that was emitted happened: it
// has an outbox event, a consumer may have acted on it, and recomputing the
// set under today's content could "un-emit" it and let it fire a second time.
// Every crossing must belong to this key and to a contribution within the
// log.
func Rebuild(ref Ref, events []Event, crossings []Crossing) (Snapshot, error) {
	s, _, err := Replay(ref, events, nil)
	if err != nil {
		return Snapshot{}, err
	}
	crossed := make([]ThresholdID, 0, len(crossings))
	for _, c := range crossings {
		switch {
		case c.Key != ref.Key:
			return Snapshot{}, integrity(ref.Key, fmt.Sprintf("was given a crossing recorded for %s", c.Key))
		case c.Seq < 1 || c.Seq > s.LastSeq:
			return Snapshot{}, integrity(ref.Key,
				fmt.Sprintf("records crossing %s at seq %d, outside its log of %d", c.Threshold.ID, c.Seq, s.LastSeq))
		}
		if err := c.Threshold.ID.Validate(); err != nil {
			return Snapshot{}, err
		}
		crossed = append(crossed, c.Threshold.ID)
	}
	slices.Sort(crossed)
	if len(slices.Compact(slices.Clone(crossed))) != len(crossed) {
		// The primary key on threshold_crossing makes this impossible in the
		// database; meeting it here means the input was not read from there.
		return Snapshot{}, integrity(ref.Key, "records one threshold crossed more than once")
	}
	s.Crossed = crossed
	return s, nil
}
