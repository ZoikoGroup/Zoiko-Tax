package fiscal

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/apd/v3"
)

// Quantity is an amount of something, with a unit.
//
// It is the third fiscal value object named by ADR-0001 control 1, alongside
// Money and Rate, and the fiscalfloat analyzer has guarded it since before it
// existed. A quantity is decimal for the same reason money is: a per-unit tax
// on 0.1 litres computed in binary floating-point is wrong by an amount nobody
// will notice until an authority does.
//
// The unit is not interpreted here. Unit semantics — which units convert to
// which, at what factor — are reference data with an authority and belong on
// the CONTENT train, exactly as ADR-0002 §7.1 places currency minor units.
// This type carries the unit so that a conversion cannot happen silently, not
// so that it can happen here.
type Quantity struct {
	amount apd.Decimal
	unit   Unit
}

// Unit is a unit of measure code.
type Unit string

// ErrUnitMismatch is returned when an operation combines two units.
var ErrUnitMismatch = errors.New("fiscal: unit mismatch")

// ParseQuantity builds a Quantity from the canonical decimal string form.
func ParseQuantity(amount string, unit Unit) (Quantity, error) {
	if unit == "" {
		return Quantity{}, errors.New("fiscal: empty unit")
	}
	d, cond, err := apd.NewFromString(amount)
	if err != nil {
		return Quantity{}, fmt.Errorf("fiscal: parse quantity %q: %w", amount, err)
	}
	if cond.Inexact() || !withinPrecision(d) {
		return Quantity{}, fmt.Errorf("fiscal: parse quantity %q: value not representable at precision %d", amount, Precision)
	}
	return Quantity{amount: *d, unit: unit}, nil
}

// Unit returns the unit of measure.
func (q Quantity) Unit() Unit { return q.unit }

// Decimal returns a copy of the amount.
func (q Quantity) Decimal() apd.Decimal { return q.amount }

// String renders the canonical decimal form.
func (q Quantity) String() string { return q.amount.Text('f') }

// IsZero reports whether the amount is exactly zero.
func (q Quantity) IsZero() bool { return q.amount.IsZero() }

// Add returns q+other. Both must be in the same unit.
func (q Quantity) Add(other Quantity) (Quantity, error) {
	if q.unit != other.unit {
		return Quantity{}, fmt.Errorf("%w: %s and %s", ErrUnitMismatch, q.unit, other.unit)
	}
	var sum apd.Decimal
	if err := Add(&sum, &q.amount, &other.amount); err != nil {
		return Quantity{}, err
	}
	return Quantity{amount: sum, unit: q.unit}, nil
}

// Sub returns q-other. Both must be in the same unit.
func (q Quantity) Sub(other Quantity) (Quantity, error) {
	if q.unit != other.unit {
		return Quantity{}, fmt.Errorf("%w: %s and %s", ErrUnitMismatch, q.unit, other.unit)
	}
	var diff apd.Decimal
	if err := Sub(&diff, &q.amount, &other.amount); err != nil {
		return Quantity{}, err
	}
	return Quantity{amount: diff, unit: q.unit}, nil
}

// ExtendPerUnit returns the money value of q at a per-unit rate, rounded under
// p. This is the one place a Quantity becomes a Money, and it takes a policy
// because it is a multiplication that has to land at a currency scale.
func (q Quantity) ExtendPerUnit(perUnit Rate, currency Currency, p RoundingPolicy) (Money, error) {
	if perUnit.Basis() != RateBasisPerUnit {
		return Money{}, fmt.Errorf("fiscal: extend per unit: rate basis is %s, not %s", perUnit.Basis(), RateBasisPerUnit)
	}
	var out apd.Decimal
	rate := perUnit.Decimal()
	if err := ApplyRate(&out, &q.amount, &rate, p); err != nil {
		return Money{}, err
	}
	return Money{amount: out, currency: currency}, nil
}

// CanonicalString renders the ADR-0011 §2.2 decimal normal form.
func (q Quantity) CanonicalString() string { return canonicalDecimal(&q.amount) }

// CanonicalString renders the ADR-0011 §2.2 decimal normal form.
func (m Money) CanonicalString() string { return canonicalDecimal(&m.amount) }

// CanonicalString renders the ADR-0011 §2.2 decimal normal form.
func (r Rate) CanonicalString() string { return canonicalDecimal(&r.value) }

// canonicalDecimal is the ADR-0011 §2.2 normal form: plain notation, no
// exponent, a single leading zero below one, a minus sign only for negatives
// and never a -0, and trailing zeros preserved to the value's significant
// scale.
//
// It lives here rather than in internal/platform/canonical because the
// one-decimal-library depguard rule confines apd to this package, and because
// the rule it implements — that 1.50 and 1.5 are different assertions about
// precision and must digest differently — is a statement about decimal
// semantics rather than about serialization.
func canonicalDecimal(d *apd.Decimal) string {
	s := d.Text('f')
	// apd renders a negative zero as "-0", which the profile forbids. This is
	// not pedantry: a credit that rounds to nothing and a debit that rounds to
	// nothing are the same amount and must digest identically.
	if d.IsZero() {
		s = strings.TrimPrefix(s, "-")
	}
	return s
}
