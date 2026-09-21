// Package fiscal holds the decimal arithmetic context and the rounding policy
// vocabulary for every fiscal quantity in the estate.
//
// It implements ADR-0002. Two rules govern everything here:
//
//   - Addition, subtraction and multiplication are exact. An inexact result is
//     a defect and surfaces as an error, not as a rounded value.
//   - Division and rate application take a RoundingPolicy. There is no default,
//     because rounding is legal content and content supplies it.
//
// No other package may construct an apd.Context. See ADR-0002 §5.1 control 1.
package fiscal

import (
	"fmt"

	"github.com/cockroachdb/apd/v3"
)

// Precision is the decimal128 coefficient width, and the width the General
// Decimal Arithmetic Specification's standard context provides. ADR-0002 §2.1.
const Precision = 34

// exactCtx performs the operations that must not round. Traps on Inexact turn a
// silently truncated fiscal amount into an error at the point it happens.
var exactCtx = &apd.Context{
	Precision:   Precision,
	MaxExponent: apd.MaxExponent,
	MinExponent: apd.MinExponent,
	Traps:       apd.Inexact | apd.Overflow | apd.Underflow,
	// Never reached: the Inexact trap fires before any rounding would. Set so
	// that the context always has a valid rounder.
	Rounding: apd.RoundHalfEven,
}

// policyCtx is the context for the one rounding event a policy authorises.
//
// Inexact is not trapped here — rounding is the intended outcome, which is the
// whole distinction ADR-0002 §3.1 draws between division and the exact
// operations. Everything else still traps: an untrapped division by zero
// returns Infinity, and an untrapped quantize past the context's precision
// returns NaN, and both of those are numbers that reach a fiscal document.
func policyCtx(p RoundingPolicy) *apd.Context {
	return &apd.Context{
		Precision:   Precision,
		MaxExponent: apd.MaxExponent,
		MinExponent: apd.MinExponent,
		Traps:       apd.DivisionByZero | apd.InvalidOperation | apd.Overflow | apd.Underflow,
		Rounding:    p.mode.rounder(),
	}
}

// Add sets dst to x+y exactly.
func Add(dst, x, y *apd.Decimal) error {
	_, err := exactCtx.Add(dst, x, y)
	return wrap("add", err)
}

// Sub sets dst to x-y exactly.
func Sub(dst, x, y *apd.Decimal) error {
	_, err := exactCtx.Sub(dst, x, y)
	return wrap("sub", err)
}

// Mul sets dst to x*y exactly.
func Mul(dst, x, y *apd.Decimal) error {
	_, err := exactCtx.Mul(dst, x, y)
	return wrap("mul", err)
}

// Quo sets dst to x/y under p.
//
// The policy parameter is required and has no default. A caller that does not
// know which rounding applies does not yet know enough to divide — see
// ADR-0002 §3.2 for why this is a compile-time obligation rather than a lint.
func Quo(dst, x, y *apd.Decimal, p RoundingPolicy) error {
	if err := p.validate(); err != nil {
		return err
	}
	// Divide at full precision first, then apply the policy's mode and scale as
	// a single explicit rounding step. Rounding only at the policy's scale keeps
	// the policy the sole rounding event, per ADR-0002 §2.5.
	if _, err := policyCtx(p).Quo(dst, x, y); err != nil {
		return wrap("quo", err)
	}
	return ApplyPolicy(dst, dst, p)
}

// ApplyRate sets dst to base*rate, rounded under p. This is the shape almost
// every tax computation takes, and routing it through one function keeps the
// rounding point visible in every call site.
func ApplyRate(dst, base, rate *apd.Decimal, p RoundingPolicy) error {
	if err := p.validate(); err != nil {
		return err
	}
	var product apd.Decimal
	if err := Mul(&product, base, rate); err != nil {
		return err
	}
	return ApplyPolicy(dst, &product, p)
}

// ApplyPolicy rounds x into dst at p's scale using p's mode. It is the only
// rounding operation exported by this package.
func ApplyPolicy(dst, x *apd.Decimal, p RoundingPolicy) error {
	if err := p.validate(); err != nil {
		return err
	}
	// Quantize to exponent -Scale: scale 2 means an exponent of -2.
	if _, err := policyCtx(p).Quantize(dst, x, -p.scale); err != nil {
		return wrap("quantize", err)
	}
	return nil
}

// withinPrecision reports whether d is a value this context can hold and
// operate on exactly.
//
// apd.NewFromString parses at arbitrary precision and reports no condition for
// a value wider than the context, so the check has to be made explicitly at
// every ingress. Without it a 35-digit amount enters the estate looking
// ordinary and first goes wrong several operations later, in the Inexact trap
// of something unrelated.
func withinPrecision(d *apd.Decimal) bool {
	return d.NumDigits() <= Precision
}

// quoInteger sets dst to the integer part of x/y, and rem to the remainder.
// Both are exact for the non-negative operands allocation feeds it, which is
// why allocation can assert totality rather than hope for it.
func quoInteger(dst, rem, x, y *apd.Decimal) error {
	intCtx := &apd.Context{
		Precision:   Precision,
		MaxExponent: apd.MaxExponent,
		MinExponent: apd.MinExponent,
		Traps:       apd.DivisionByZero | apd.InvalidOperation | apd.Overflow | apd.Underflow,
		Rounding:    apd.RoundDown,
	}
	if _, err := intCtx.QuoInteger(dst, x, y); err != nil {
		return wrap("quo-integer", err)
	}
	if _, err := intCtx.Rem(rem, x, y); err != nil {
		return wrap("rem", err)
	}
	return nil
}

func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("fiscal: %s: %w", op, err)
}
