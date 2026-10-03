package fiscal

import (
	"errors"
	"fmt"

	"github.com/cockroachdb/apd/v3"
)

// This file holds the exact operations ZTAX-DET-001 needs and that the
// one-decimal-library rule keeps out of every other package: comparison,
// negation, unrounded products, and the full-precision Factor that inclusive
// extraction carries (DET-001 §7.3).
//
// Everything here is exact. The one exception is DivFactor, which takes a
// policy for the same reason Quo does: division is the operation that cannot
// be exact, so it is the operation that names its rounding.

// ErrDivisionByZero is returned when a division's divisor is zero.
var ErrDivisionByZero = errors.New("fiscal: division by zero")

// Neg returns -m.
func (m Money) Neg() Money {
	var out apd.Decimal
	out.Neg(&m.amount)
	return Money{amount: out, currency: m.currency}
}

// Sign returns -1, 0 or +1.
func (m Money) Sign() int { return m.amount.Sign() }

// Cmp compares m with other: -1 if m < other, 0 if equal, +1 if m > other.
// Equality is numeric, so 1.50 and 1.5 compare equal; the canonical form is
// where they differ. Both must be in the same currency.
func (m Money) Cmp(other Money) (int, error) {
	if m.currency != other.currency {
		return 0, fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.currency, other.currency)
	}
	return m.amount.Cmp(&other.amount), nil
}

// Min returns the smaller of m and other.
func (m Money) Min(other Money) (Money, error) {
	c, err := m.Cmp(other)
	if err != nil {
		return Money{}, err
	}
	if c <= 0 {
		return m, nil
	}
	return other, nil
}

// Max returns the larger of m and other.
func (m Money) Max(other Money) (Money, error) {
	c, err := m.Cmp(other)
	if err != nil {
		return Money{}, err
	}
	if c >= 0 {
		return m, nil
	}
	return other, nil
}

// Zero returns a zero amount in m's currency, at m's scale. It is how a
// determination writes "this component did not apply" (DET-001 §5.6) without
// parsing a literal.
func (m Money) Zero() Money {
	var out apd.Decimal
	out.SetFinite(0, m.amount.Exponent)
	return Money{amount: out, currency: m.currency}
}

// Times returns m*r exactly, with no rounding. It is the raw_i of DET-001 §6.1:
// the full-precision product that a policy rounds once, later, at the point
// content names. A product too wide for the context is an error, never a
// silently rounded amount.
func (m Money) Times(r Rate) (Money, error) {
	var out apd.Decimal
	if err := Mul(&out, &m.amount, &r.value); err != nil {
		return Money{}, err
	}
	return Money{amount: out, currency: m.currency}, nil
}

// TimesQuantity returns m*q exactly: a per-unit amount extended over a
// quantity, the SPECIFIC rate kind of DET-001 §4.1.
func (m Money) TimesQuantity(q Quantity) (Money, error) {
	var out apd.Decimal
	if err := Mul(&out, &m.amount, &q.amount); err != nil {
		return Money{}, err
	}
	return Money{amount: out, currency: m.currency}, nil
}

// TimesFactor returns m*f exactly.
func (m Money) TimesFactor(f Factor) (Money, error) {
	var out apd.Decimal
	if err := Mul(&out, &m.amount, &f.value); err != nil {
		return Money{}, err
	}
	return Money{amount: out, currency: m.currency}, nil
}

// DivFactor returns m/f rounded under p. It is step 3 of DET-001 §7.4,
// N := round(G / M, extractionPolicy), and it is the only rounding inclusive
// extraction performs.
func (m Money) DivFactor(f Factor, p RoundingPolicy) (Money, error) {
	if f.value.IsZero() {
		return Money{}, ErrDivisionByZero
	}
	var out apd.Decimal
	if err := Quo(&out, &m.amount, &f.value, p); err != nil {
		return Money{}, err
	}
	return Money{amount: out, currency: m.currency}, nil
}

// Factor is a dimensionless multiplier carried at full precision.
//
// It exists for DET-001 §7.3: the per-component multiplier on net m_i and the
// effective multiplier M are "intermediate quantities with no legal existence"
// and are never rounded. A Rate would be the wrong type for them — a Rate has a
// basis and is content, and these are derived — and a bare decimal is not
// available outside this package. So they get a type whose only operations are
// exact.
type Factor struct {
	value apd.Decimal
}

// FactorOne is the multiplicative identity, the 1 in M = 1 + Σ m_i.
func FactorOne() Factor {
	var f Factor
	f.value.SetInt64(1)
	return f
}

// FactorOf reads a rate's value as a factor, dropping its basis. The caller
// has already decided the basis is right for the use; DET-001 §7.3 only
// admits proportional rates into the multiplier.
func FactorOf(r Rate) Factor { return Factor{value: r.value} }

// Add returns f+g exactly.
func (f Factor) Add(g Factor) (Factor, error) {
	var out Factor
	if err := Add(&out.value, &f.value, &g.value); err != nil {
		return Factor{}, err
	}
	return out, nil
}

// Mul returns f*g exactly.
func (f Factor) Mul(g Factor) (Factor, error) {
	var out Factor
	if err := Mul(&out.value, &f.value, &g.value); err != nil {
		return Factor{}, err
	}
	return out, nil
}

// IsZero reports whether f is exactly zero.
func (f Factor) IsZero() bool { return f.value.IsZero() }

// Sign returns -1, 0 or +1.
func (f Factor) Sign() int { return f.value.Sign() }

// String renders the canonical decimal form, for the trace.
func (f Factor) String() string { return canonicalDecimal(&f.value) }

// AllocateByFactors is Allocate with full-precision weights: DET-001 §7.4
// step 5 allocates the extracted tax across the inclusive components weighted
// by their multipliers m_i, which have no currency and must not be rounded to
// become weights. Same method, same tie-break on ordinal, same totality.
func AllocateByFactors(total Money, weights []Factor, p RoundingPolicy) ([]Money, error) {
	ws := make([]Money, len(weights))
	for i, w := range weights {
		ws[i] = Money{amount: w.value, currency: total.currency}
	}
	return Allocate(total, ws, p)
}
