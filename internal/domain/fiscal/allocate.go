package fiscal

import (
	"errors"
	"fmt"
	"sort"

	"github.com/cockroachdb/apd/v3"
)

// ErrNoWeight is returned when a non-zero total is allocated across weights
// that sum to zero. There is no proportion to divide by, and picking a line to
// carry the amount would be an arbitrary fiscal decision made in Go.
var ErrNoWeight = errors.New("fiscal: weights sum to zero")

// Allocate distributes total across the given weights and returns one part per
// weight, in the same order. ADR-0002 §2.6.
//
// The method is largest-remainder: each part takes the whole number of minor
// units its proportion earns, and the residual units left over by that
// truncation are handed out one each, in order of the largest fractional
// remainder, ties broken on ascending ordinal. The ordinal is the index in
// weights, which is part of the canonical input (ADR-0011), so the same
// document allocates the same way on every replay.
//
// Allocation is total: the parts sum to total materialised at p's scale,
// exactly, for every input this function accepts. That is asserted as a
// property test rather than argued for here.
//
// Three properties of the implementation are worth knowing:
//
//   - The only rounding event is materialising total at p's scale. Everything
//     after it is exact integer arithmetic on minor units, so no part is the
//     result of a second, unnamed rounding (ADR-0002 §2.5).
//   - A negative total is allocated by magnitude and the parts carry its sign.
//     Allocation is therefore symmetric about zero, whatever the mode does at
//     the rounding step — a credit note allocates like the invoice it reverses.
//   - Weights may not be negative. A negative weight has no fractional
//     remainder that can be ranked against a positive one, and the largest-
//     remainder guarantee does not survive the mix.
func Allocate(total Money, weights []Money, p RoundingPolicy) ([]Money, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	if len(weights) == 0 {
		return nil, errors.New("fiscal: allocate: no weights")
	}
	for i, w := range weights {
		if w.currency != total.currency {
			return nil, fmt.Errorf("%w: total %s, weight %d %s", ErrCurrencyMismatch, total.currency, i, w.currency)
		}
		if w.amount.Negative {
			return nil, fmt.Errorf("fiscal: allocate: weight %d is negative (%s)", i, w.String())
		}
	}

	rounded, err := total.Round(p)
	if err != nil {
		return nil, err
	}

	// Work in whole minor units at p's scale, on the magnitude. units is an
	// integer by construction: rounded sits at exponent -Scale, and scaling by
	// 10^Scale shifts it to exponent 0 without touching the coefficient.
	var units apd.Decimal
	if err := Mul(&units, &rounded.amount, apd.New(1, p.scale)); err != nil {
		return nil, err
	}
	negative := units.Negative
	units.Abs(&units)

	var weightSum apd.Decimal
	for i := range weights {
		if err := Add(&weightSum, &weightSum, &weights[i].amount); err != nil {
			return nil, err
		}
	}

	parts := make([]apd.Decimal, len(weights))
	remainders := make([]apd.Decimal, len(weights))

	if weightSum.IsZero() {
		// Nothing to divide by. A zero total over zero weight is the one case
		// with an unambiguous answer: every part is zero.
		if !units.IsZero() {
			return nil, fmt.Errorf("%w: cannot allocate %s", ErrNoWeight, total.String())
		}
	} else {
		var allocated apd.Decimal
		for i := range weights {
			// share = units * wᵢ / Σw, split into its integer part and the
			// remainder that ranks it against the other lines. Both are exact.
			var share apd.Decimal
			if err := Mul(&share, &units, &weights[i].amount); err != nil {
				return nil, err
			}
			if err := quoInteger(&parts[i], &remainders[i], &share, &weightSum); err != nil {
				return nil, err
			}
			if err := Add(&allocated, &allocated, &parts[i]); err != nil {
				return nil, err
			}
		}

		residual, err := residualUnits(&units, &allocated, len(weights))
		if err != nil {
			return nil, err
		}
		for _, idx := range rankByRemainder(remainders, residual) {
			if err := Add(&parts[idx], &parts[idx], apd.New(1, 0)); err != nil {
				return nil, err
			}
		}
	}

	// Back from minor units to an amount at p's scale, and restore the sign.
	out := make([]Money, len(parts))
	for i := range parts {
		var amount apd.Decimal
		if err := Mul(&amount, &parts[i], apd.New(1, -p.scale)); err != nil {
			return nil, err
		}
		if negative {
			amount.Neg(&amount)
		}
		if err := ApplyPolicy(&amount, &amount, p); err != nil {
			return nil, err
		}
		out[i] = Money{amount: amount, currency: total.currency}
	}
	return out, nil
}

// AllocateEvenly splits total into n parts of equal weight. It is Allocate
// with every weight one, and it exists because an even split is common enough
// that constructing the weights at each call site invites a mistake in them.
func AllocateEvenly(total Money, n int, p RoundingPolicy) ([]Money, error) {
	if n <= 0 {
		return nil, fmt.Errorf("fiscal: allocate evenly: %d parts", n)
	}
	weights := make([]Money, n)
	for i := range weights {
		weights[i] = Money{currency: total.currency}
		weights[i].amount.SetInt64(1)
	}
	return Allocate(total, weights, p)
}

// residualUnits is the count of minor units that truncation left undistributed.
// It is bounded by the number of parts: each part lost strictly less than one
// unit to truncation. A value outside that range means the arithmetic above is
// wrong, and it is better to say so than to return a plausible allocation.
func residualUnits(units, allocated *apd.Decimal, n int) (int, error) {
	var residual apd.Decimal
	if err := Sub(&residual, units, allocated); err != nil {
		return 0, err
	}
	count, err := residual.Int64()
	if err != nil {
		return 0, fmt.Errorf("fiscal: allocate: residual %s is not a unit count: %w", residual.Text('f'), err)
	}
	if count < 0 || count > int64(n) {
		return 0, fmt.Errorf("fiscal: allocate: residual %d outside [0,%d]", count, n)
	}
	return int(count), nil
}

// rankByRemainder returns the indices of the n largest remainders, ordered by
// remainder descending and by ordinal ascending within a tie. ADR-0002 §2.6.
func rankByRemainder(remainders []apd.Decimal, n int) []int {
	order := make([]int, len(remainders))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		i, j := order[a], order[b]
		if cmp := remainders[i].Cmp(&remainders[j]); cmp != 0 {
			return cmp > 0
		}
		return i < j
	})
	return order[:n]
}
