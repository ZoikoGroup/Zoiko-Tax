package fiscal_test

import (
	"strings"
	"testing"

	"github.com/cockroachdb/apd/v3"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

// The golden corpus owns the arithmetic assertions (ADR-0002 §2.7). What this
// file asserts is the shape of the API: which operations refuse to proceed,
// and what happens to a caller that tries to reach around the policy.

func TestExactOperationsRefuseToRound(t *testing.T) {
	// 1E+30 + 1E-30 needs 61 significant digits. ADR-0002 §3.1: the failure a
	// trap prevents is not a wrong answer, it is a plausible one.
	big := decimal(t, "1000000000000000000000000000000")
	small := decimal(t, "0.000000000000000000000000000001")

	var out apd.Decimal
	err := fiscal.Add(&out, big, small)
	if err == nil {
		t.Fatalf("Add lost precision silently and returned %s", out.Text('f'))
	}
	if !strings.Contains(err.Error(), "fiscal: add") {
		t.Errorf("error %q does not name the operation", err)
	}
}

func TestZeroValuePolicyIsRejectedByEveryRoundingOperation(t *testing.T) {
	// ADR-0002 §2.3: a RoundingPolicy composite literal is not a policy. The
	// zero value is what a caller reaching around content ends up holding, and
	// every operation that would round has to refuse it.
	var unset fiscal.RoundingPolicy
	x, y := decimal(t, "10.00"), decimal(t, "4")

	operations := map[string]func(dst *apd.Decimal) error{
		"Quo":         func(dst *apd.Decimal) error { return fiscal.Quo(dst, x, y, unset) },
		"ApplyRate":   func(dst *apd.Decimal) error { return fiscal.ApplyRate(dst, x, y, unset) },
		"ApplyPolicy": func(dst *apd.Decimal) error { return fiscal.ApplyPolicy(dst, x, unset) },
	}
	for name, operate := range operations {
		t.Run(name, func(t *testing.T) {
			var out apd.Decimal
			err := operate(&out)
			if err == nil {
				t.Fatalf("%s accepted the zero-value policy and returned %s", name, out.Text('f'))
			}
			if !strings.Contains(err.Error(), "unknown rounding mode") {
				t.Errorf("error %q does not say why the policy is unusable", err)
			}
		})
	}
}

func TestQuoTrapsDivisionByZero(t *testing.T) {
	// Untrapped, the specification's answer here is Infinity. An infinite tax
	// amount is a number, and a number reaches a fiscal document.
	policy := fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 2)

	var out apd.Decimal
	err := fiscal.Quo(&out, decimal(t, "1.00"), decimal(t, "0"), policy)
	if err == nil {
		t.Fatalf("division by zero returned %s", out.Text('f'))
	}
	if !strings.Contains(err.Error(), "division by zero") {
		t.Errorf("error %q does not name the condition", err)
	}
}

func TestApplyPolicyRefusesWhatItCannotRepresent(t *testing.T) {
	// Quantizing a 41-digit value to scale 2 needs 43 digits against a context
	// of 34. Rounding it to something that fits would be a silent change of
	// magnitude, so the operation refuses instead.
	policy := fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 2)

	var out apd.Decimal
	err := fiscal.ApplyPolicy(&out, oversizedDecimal(t, "10000000000000000000000000000000000000000"), policy)
	if err == nil {
		t.Fatalf("quantize past precision returned %s", out.Text('f'))
	}
	if !strings.Contains(err.Error(), "invalid operation") {
		t.Errorf("error %q does not name the condition", err)
	}
}

func TestApplyRateRoundsOnlyOnce(t *testing.T) {
	// ADR-0002 §2.5: the product is carried at full precision and the policy's
	// rounding is the only rounding event. 1.00 x 0.4449 is exactly 0.444900,
	// which is 0.44 at scale 2.
	//
	// An implementation that materialised the intermediate at scale 3 first —
	// round-as-you-go, §4.3 — would get 0.445 and then 0.45. One cent, from a
	// rounding point no authority named.
	base, rate := decimal(t, "1.00"), decimal(t, "0.4449")

	var out apd.Decimal
	if err := fiscal.ApplyRate(&out, base, rate, fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 2)); err != nil {
		t.Fatalf("apply rate: %v", err)
	}
	if got := out.Text('f'); got != "0.44" {
		t.Fatalf("got %s, want 0.44 — an intermediate was rounded before the policy applied", got)
	}

	var intermediate apd.Decimal
	if err := fiscal.ApplyRate(&intermediate, base, rate, fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 3)); err != nil {
		t.Fatalf("apply rate at scale 3: %v", err)
	}
	var twice apd.Decimal
	if err := fiscal.ApplyPolicy(&twice, &intermediate, fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 2)); err != nil {
		t.Fatalf("round twice: %v", err)
	}
	if got := twice.Text('f'); got != "0.45" {
		t.Fatalf("the round-as-you-go path gives %s; the case no longer demonstrates what it claims", got)
	}
}

// oversizedDecimal parses a value the context cannot hold. ParseMoney refuses
// such a value at the ingress, which is where it should be refused; this exists
// so that a test can still put one in front of an operation and see what the
// operation itself does with it.
func oversizedDecimal(t *testing.T, s string) *apd.Decimal {
	t.Helper()
	d, _, err := apd.NewFromString(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	if d.NumDigits() <= fiscal.Precision {
		t.Fatalf("%q fits in precision %d; the case no longer tests what it claims", s, fiscal.Precision)
	}
	return d
}

func decimal(t *testing.T, s string) *apd.Decimal {
	t.Helper()
	d, cond, err := apd.NewFromString(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	if cond.Inexact() || d.NumDigits() > fiscal.Precision {
		t.Fatalf("%q is not representable at precision %d", s, fiscal.Precision)
	}
	return d
}
