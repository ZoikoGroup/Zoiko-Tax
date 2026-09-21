// Package fiscaltest constructs fiscal values for tests.
//
// ADR-0002 §2.3 puts the origin of every RoundingPolicy in a RuleVersion inside
// a signed content bundle. Tests have no bundle, so this package stands in for
// one — and it does so by building the same JSON a bundle carries and decoding
// it through fiscal.DecodeRoundingPolicy, so a test exercises the production
// ingress rather than a parallel one.
//
// Production code may not import this package. The gate is depguard rule
// policy-literals-are-test-only in .golangci.yml; the import of testing is the
// second signal, since it drags the testing flag set into any binary that takes
// this dependency.
package fiscaltest

import (
	"encoding/json"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
)

// Policy builds the rounding policy a content bundle would have supplied.
func Policy(tb testing.TB, mode fiscal.RoundingMode, scale int32, basis fiscal.RoundingBasis) fiscal.RoundingPolicy {
	tb.Helper()
	encoded, err := json.Marshal(struct {
		Mode  fiscal.RoundingMode  `json:"mode"`
		Scale int32                `json:"scale"`
		Basis fiscal.RoundingBasis `json:"basis"`
	}{Mode: mode, Scale: scale, Basis: basis})
	if err != nil {
		tb.Fatalf("fiscaltest: encode policy %s@%d/%s: %v", mode, scale, basis, err)
	}
	p, err := fiscal.DecodeRoundingPolicy(encoded)
	if err != nil {
		tb.Fatalf("fiscaltest: %v", err)
	}
	return p
}

// LinePolicy is the policy most tests want: round at the line, at the scale
// given, with the mode given.
func LinePolicy(tb testing.TB, mode fiscal.RoundingMode, scale int32) fiscal.RoundingPolicy {
	tb.Helper()
	return Policy(tb, mode, scale, fiscal.BasisLine)
}

// Money parses an amount that the test asserts is well-formed.
func Money(tb testing.TB, amount string, currency fiscal.Currency) fiscal.Money {
	tb.Helper()
	m, err := fiscal.ParseMoney(amount, currency)
	if err != nil {
		tb.Fatalf("fiscaltest: %v", err)
	}
	return m
}

// Amounts parses several amounts in one currency — line nets, allocation
// weights, expected parts.
func Amounts(tb testing.TB, currency fiscal.Currency, amounts ...string) []fiscal.Money {
	tb.Helper()
	out := make([]fiscal.Money, len(amounts))
	for i, a := range amounts {
		out[i] = Money(tb, a, currency)
	}
	return out
}

// Rate parses a rate that the test asserts is well-formed.
func Rate(tb testing.TB, value string, basis fiscal.RateBasis) fiscal.Rate {
	tb.Helper()
	r, err := fiscal.ParseRate(value, basis)
	if err != nil {
		tb.Fatalf("fiscaltest: %v", err)
	}
	return r
}

// Strings renders monies for comparison in a test failure message, and for
// asserting a whole allocation in one line.
func Strings(values []fiscal.Money) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = v.String()
	}
	return out
}
