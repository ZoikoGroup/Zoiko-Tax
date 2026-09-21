package fiscal

import (
	"errors"
	"fmt"

	"github.com/cockroachdb/apd/v3"
)

// ErrCurrencyMismatch is returned when an operation combines two currencies.
var ErrCurrencyMismatch = errors.New("fiscal: currency mismatch")

// Currency is an ISO 4217 alphabetic code.
//
// The minor-unit exponent for a currency is reference data with an authority,
// so it lives on the CONTENT train rather than in a map here. See ADR-0002 §7.1.
type Currency string

// Money is an amount in a currency. It is the canonical fiscal value object:
// decimal plus ISO 4217, no binary float, per ADR-0001 C1.
//
// The amount is unexported. There is no accessor returning a float64, and no
// method on this type produces one, so a Money cannot reach a binary-float
// expression by any route the compiler permits.
type Money struct {
	amount   apd.Decimal
	currency Currency
}

// ParseMoney builds a Money from the canonical decimal string form
// (ADR-0011 §2.2). Parsing from a string rather than from a numeric type is
// deliberate: it is the only ingress that cannot have passed through a float.
func ParseMoney(amount string, currency Currency) (Money, error) {
	if currency == "" {
		return Money{}, errors.New("fiscal: empty currency")
	}
	d, cond, err := apd.NewFromString(amount)
	if err != nil {
		return Money{}, fmt.Errorf("fiscal: parse money %q: %w", amount, err)
	}
	if cond.Inexact() || !withinPrecision(d) {
		return Money{}, fmt.Errorf("fiscal: parse money %q: value not representable at precision %d", amount, Precision)
	}
	return Money{amount: *d, currency: currency}, nil
}

// Currency returns the ISO 4217 code.
func (m Money) Currency() Currency { return m.currency }

// Decimal returns a copy of the amount for arithmetic through this package's
// operations. It is a copy so that a caller cannot mutate a Money in place.
func (m Money) Decimal() apd.Decimal { return m.amount }

// String renders the canonical decimal form: plain notation, no exponent,
// trailing zeros preserved because scale is semantic (ADR-0011 §2.2).
func (m Money) String() string { return m.amount.Text('f') }

// IsZero reports whether the amount is exactly zero.
func (m Money) IsZero() bool { return m.amount.IsZero() }

// Add returns m+other. Both must be in the same currency.
func (m Money) Add(other Money) (Money, error) {
	if m.currency != other.currency {
		return Money{}, fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.currency, other.currency)
	}
	var sum apd.Decimal
	if err := Add(&sum, &m.amount, &other.amount); err != nil {
		return Money{}, err
	}
	return Money{amount: sum, currency: m.currency}, nil
}

// Sub returns m-other. Both must be in the same currency.
func (m Money) Sub(other Money) (Money, error) {
	if m.currency != other.currency {
		return Money{}, fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.currency, other.currency)
	}
	var diff apd.Decimal
	if err := Sub(&diff, &m.amount, &other.amount); err != nil {
		return Money{}, err
	}
	return Money{amount: diff, currency: m.currency}, nil
}

// ApplyRate returns m*rate rounded under p — the shape of a tax computation.
// The policy is required; see ADR-0002 §2.2.
func (m Money) ApplyRate(rate Rate, p RoundingPolicy) (Money, error) {
	var out apd.Decimal
	rateDec := rate.Decimal()
	if err := ApplyRate(&out, &m.amount, &rateDec, p); err != nil {
		return Money{}, err
	}
	return Money{amount: out, currency: m.currency}, nil
}

// Round returns m rounded under p.
func (m Money) Round(p RoundingPolicy) (Money, error) {
	var out apd.Decimal
	if err := ApplyPolicy(&out, &m.amount, p); err != nil {
		return Money{}, err
	}
	return Money{amount: out, currency: m.currency}, nil
}

// Rate is a proportion with an explicit basis. A bare decimal is not a rate:
// 0.06 means nothing until it is known whether it applies to a net amount, a
// gross amount or a per-unit quantity.
type Rate struct {
	value apd.Decimal
	basis RateBasis
}

// RateBasis names what a Rate is a proportion of.
type RateBasis string

// The bases a rate may be a proportion of. NET and GROSS differ by whether the
// rate applies before or after tax already charged; COMPOUND names a rate that
// applies to a base including another tax, which is the tax-on-tax shape whose
// ordering DET-001 has to settle (ADR-0005 §7.1).
const (
	RateBasisNet      RateBasis = "NET"
	RateBasisGross    RateBasis = "GROSS"
	RateBasisPerUnit  RateBasis = "PER_UNIT"
	RateBasisCompound RateBasis = "COMPOUND"
)

// ParseRate builds a Rate from the canonical decimal string form.
func ParseRate(value string, basis RateBasis) (Rate, error) {
	switch basis {
	case RateBasisNet, RateBasisGross, RateBasisPerUnit, RateBasisCompound:
	default:
		return Rate{}, fmt.Errorf("fiscal: unknown rate basis %q", basis)
	}
	d, cond, err := apd.NewFromString(value)
	if err != nil {
		return Rate{}, fmt.Errorf("fiscal: parse rate %q: %w", value, err)
	}
	if cond.Inexact() || !withinPrecision(d) {
		return Rate{}, fmt.Errorf("fiscal: parse rate %q: value not representable at precision %d", value, Precision)
	}
	return Rate{value: *d, basis: basis}, nil
}

// Decimal returns a copy of the rate value.
func (r Rate) Decimal() apd.Decimal { return r.value }

// Basis returns what the rate applies to.
func (r Rate) Basis() RateBasis { return r.basis }

// String renders the canonical decimal form.
func (r Rate) String() string { return r.value.Text('f') }
