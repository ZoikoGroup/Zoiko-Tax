package evidence

import (
	"fmt"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Input is the canonical input to one determination: the named values the
// evaluation frame carries (ADR-0005 §2.5), and nothing else.
//
// It is deliberately the frame's shape rather than a transaction's. Which
// fields a line carries, and what they are called, is decided by content — the
// eu-vat pack reads "line.netAmount", another pack reads something else — so
// the evidence records the values under the names content asked for. A
// transaction-shaped input with Go field names would be tax logic written in
// Go, which ZTAX-DET-001 §0.2 forbids.
type Input struct {
	Money      map[string]fiscal.Money
	Rates      map[string]fiscal.Rate
	Quantities map[string]fiscal.Quantity
	Flags      map[string]bool
	Strings    map[string]string
}

// Validate refuses an input with nothing in it, or with a nameless value.
func (in Input) Validate() error {
	if len(in.Money)+len(in.Rates)+len(in.Quantities)+len(in.Flags)+len(in.Strings) == 0 {
		return fmt.Errorf("evidence: envelope carries no canonical input")
	}
	for _, names := range [][]string{keys(in.Money), keys(in.Rates), keys(in.Quantities), keys(in.Flags), keys(in.Strings)} {
		for _, n := range names {
			if n == "" {
				return fmt.Errorf("evidence: canonical input carries a value with no name")
			}
		}
	}
	return nil
}

// Canonical renders the input.
//
// A section with no values is omitted rather than written as an empty object
// (ADR-0011 P3): "this transaction carried no rates" and "the rates section was
// empty" are the same fact here, and there must be only one encoding of it.
// Object member order is canon/v1's, so the order of the maps is irrelevant.
func (in Input) Canonical() canonical.Value {
	return canonical.Object(
		canonical.F("money", moneySection(in.Money, false)),
		canonical.F("rates", section(in.Rates, func(r fiscal.Rate) canonical.Value {
			return canonical.Object(
				canonical.F("value", canonical.Rate(r)),
				canonical.F("basis", canonical.String(string(r.Basis()))),
			)
		})),
		canonical.F("quantities", section(in.Quantities, func(q fiscal.Quantity) canonical.Value {
			return canonical.Object(
				canonical.F("value", canonical.Quantity(q)),
				canonical.F("unit", canonical.String(string(q.Unit()))),
			)
		})),
		canonical.F("flags", section(in.Flags, canonical.Bool)),
		canonical.F("strings", section(in.Strings, canonical.String)),
	)
}

// moneyValue is the one rendering of an amount in evidence: the decimal as a
// string (ADR-0011 P1) beside its currency, never one without the other.
func moneyValue(m fiscal.Money) canonical.Value {
	return canonical.Object(
		canonical.F("amount", canonical.Money(m)),
		canonical.F("currency", canonical.String(string(m.Currency()))),
	)
}

// moneySection renders a map of amounts. keepEmpty writes an empty map as {}
// rather than omitting it, for the one place that distinction is a fact — the
// accumulator read set.
func moneySection(m map[string]fiscal.Money, keepEmpty bool) canonical.Value {
	if len(m) == 0 && keepEmpty {
		return canonical.Object()
	}
	return section(m, moneyValue)
}

func section[T any](m map[string]T, render func(T) canonical.Value) canonical.Value {
	if len(m) == 0 {
		return canonical.Absent()
	}
	fields := make([]canonical.Field, 0, len(m))
	for name, v := range m {
		fields = append(fields, canonical.F(name, render(v)))
	}
	return canonical.Object(fields...)
}

func keys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
