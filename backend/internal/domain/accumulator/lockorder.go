package accumulator

import "slices"

// LockOrder is the canonical order in which accumulator locks are acquired:
// ascending, by byte, with duplicates removed (ADR-0004 §2.3).
//
// This function is the entirety of the deadlock-avoidance strategy. A commit
// that touches several accumulators — a jurisdiction cap, a customer
// de-minimis, a registration threshold — takes their locks in this order,
// always, with no exception for "we already have this one". Two commits that
// both do so cannot each hold a lock the other is waiting for, because both
// are climbing the same ladder in the same direction.
//
// It is one function rather than a documented convention because convention
// fails at the first multi-accumulator rule written by someone who has not
// read the ADR (§3.4). The adapter's LockAll is the only caller that
// acquires, and it routes every acquisition through here; a direct
// FOR UPDATE on accumulator_snapshot anywhere else fails
// TestNoAccumulatorLockOutsideTheRepository (ADR-0004 §5.1 control 1).
//
// Byte order is the right order, rather than a collation, because keys are
// printable ASCII (see Key): for those, Go, PostgreSQL's "C" collation and
// every locale agree, so no database setting can make two processes disagree
// about which lock comes first.
//
// The input is not modified. The result is a new slice, and is empty, not
// nil, for an empty input.
func LockOrder(keys []Key) []Key {
	out := slices.Clone(keys)
	if out == nil {
		out = []Key{}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
