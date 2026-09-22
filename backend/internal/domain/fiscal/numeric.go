package fiscal

import (
	"fmt"
	"math/big"

	"github.com/cockroachdb/apd/v3"
)

// NumericParts is the exact representation of a decimal at the database
// boundary: an unscaled integer coefficient and a base-10 exponent.
//
// This type exists so that the persistence layer can map PostgreSQL NUMERIC
// without importing apd. That is not a workaround for the depguard rule — it is
// the shape of the guarantee ADR-0001 control 2 asks for. PostgreSQL NUMERIC
// and apd.Decimal are both (sign, integer coefficient, base-10 exponent), so a
// conversion through those three fields is exact by construction, and there is
// no arithmetic in it for a float to hide in. A codec that went via a string,
// or worse via a float64, would need a test to show it was lossless; this one
// needs a test to show the fields line up, which is a much smaller claim.
//
// Coeff is always non-negative; Negative carries the sign. That matches both
// apd.Decimal and pgtype.Numeric, and it means a negative zero survives the
// round trip as the distinct thing it is — the canonical form drops the sign at
// serialization (ADR-0011 §2.2), but the storage layer does not get to decide
// that.
type NumericParts struct {
	// Coeff is the unscaled coefficient, always non-negative. Never nil for a
	// valid value.
	Coeff *big.Int
	// Exp is the base-10 exponent: the value is Coeff * 10^Exp.
	Exp int32
	// Negative is the sign.
	Negative bool
}

// ErrNotFinite is returned when a value that must be stored is an infinity or a
// NaN. Neither is a fiscal amount, and neither may reach a NUMERIC column —
// PostgreSQL would accept 'NaN'::numeric, and a NaN tax amount that compares
// false against everything is far worse than a rejected write.
var ErrNotFinite = fmt.Errorf("fiscal: value is not finite")

// numericParts converts an apd.Decimal to its exact integer parts.
func numericParts(d *apd.Decimal) (NumericParts, error) {
	if d.Form != apd.Finite {
		return NumericParts{}, fmt.Errorf("%w: form %v", ErrNotFinite, d.Form)
	}
	// MathBigInt returns a copy, so the caller cannot reach back into the
	// decimal through the coefficient.
	return NumericParts{
		Coeff:    d.Coeff.MathBigInt(),
		Exp:      d.Exponent,
		Negative: d.Negative,
	}, nil
}

// fromNumericParts rebuilds an apd.Decimal from exact integer parts.
func fromNumericParts(p NumericParts) (apd.Decimal, error) {
	if p.Coeff == nil {
		return apd.Decimal{}, fmt.Errorf("fiscal: numeric parts have no coefficient")
	}
	if p.Coeff.Sign() < 0 {
		return apd.Decimal{}, fmt.Errorf("fiscal: numeric coefficient is negative; the sign belongs in Negative")
	}
	var d apd.Decimal
	d.Form = apd.Finite
	d.Coeff.SetMathBigInt(p.Coeff)
	d.Exponent = p.Exp
	d.Negative = p.Negative
	if !withinPrecision(&d) {
		return apd.Decimal{}, fmt.Errorf("fiscal: stored value has %d digits, above precision %d", d.NumDigits(), Precision)
	}
	return d, nil
}

// NumericParts returns the exact database representation of the amount.
func (m Money) NumericParts() (NumericParts, error) { return numericParts(&m.amount) }

// NumericParts returns the exact database representation of the rate.
func (r Rate) NumericParts() (NumericParts, error) { return numericParts(&r.value) }

// NumericParts returns the exact database representation of the quantity.
func (q Quantity) NumericParts() (NumericParts, error) { return numericParts(&q.amount) }

// MoneyFromNumeric rebuilds a Money from what the database returned.
func MoneyFromNumeric(p NumericParts, currency Currency) (Money, error) {
	if currency == "" {
		return Money{}, fmt.Errorf("fiscal: empty currency")
	}
	d, err := fromNumericParts(p)
	if err != nil {
		return Money{}, err
	}
	return Money{amount: d, currency: currency}, nil
}

// RateFromNumeric rebuilds a Rate from what the database returned.
func RateFromNumeric(p NumericParts, basis RateBasis) (Rate, error) {
	switch basis {
	case RateBasisNet, RateBasisGross, RateBasisPerUnit, RateBasisCompound:
	default:
		return Rate{}, fmt.Errorf("fiscal: unknown rate basis %q", basis)
	}
	d, err := fromNumericParts(p)
	if err != nil {
		return Rate{}, err
	}
	return Rate{value: d, basis: basis}, nil
}

// QuantityFromNumeric rebuilds a Quantity from what the database returned.
func QuantityFromNumeric(p NumericParts, unit Unit) (Quantity, error) {
	if unit == "" {
		return Quantity{}, fmt.Errorf("fiscal: empty unit")
	}
	d, err := fromNumericParts(p)
	if err != nil {
		return Quantity{}, err
	}
	return Quantity{amount: d, unit: unit}, nil
}
