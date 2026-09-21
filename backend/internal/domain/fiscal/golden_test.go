package fiscal_test

import (
	"strings"
	"testing"

	"github.com/cockroachdb/apd/v3"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

// goldenRoot is where the vector sets live. They sit under testdata/ rather
// than beside the package because they are an estate artifact that the Python
// cross-check reads too — ADR-0002 §2.7 makes them a build artifact, not a
// fixture of this package. Reading them is fiscaltest's job: internal/domain
// performs no I/O, tests included (ADR-0007 §2.5).
const goldenRoot = "../../../testdata/golden/decimal"

// vectorCurrency is ISO 4217's code reserved for testing. Vectors assert
// arithmetic, so naming a real currency would imply a minor-unit claim the
// vector is not making.
const vectorCurrency fiscal.Currency = "XTS"

// TestGoldenDecimalVectors is tier 2 of ADR-0018 §2.1: the assertion that this
// runtime produces the registered answer for every case in the corpus, exactly.
// There is no tolerance comparison anywhere in this file, by design.
func TestGoldenDecimalVectors(t *testing.T) {
	for _, set := range fiscaltest.LoadGoldenSets(t, goldenRoot) {
		t.Run(set.Set, func(t *testing.T) {
			for _, c := range set.Cases {
				t.Run(c.ID, func(t *testing.T) {
					// ADR-0018 §3.1: a case with no oracle proves that we are
					// consistent with ourselves, which is not what pack
					// certification claims.
					if strings.TrimSpace(c.Oracle) == "" {
						t.Fatal("case carries no oracle")
					}
					runVector(t, c)
				})
			}
		})
	}
}

// TestGoldenDecimalVectorsAreRegistered is the other half of §2.7: a vector set
// that can be edited without ceremony is a fixture. Every set carries a
// registered digest, and re-baselining one is a reviewed act (ADR-0002 §6).
func TestGoldenDecimalVectorsAreRegistered(t *testing.T) {
	fiscaltest.CheckGoldenRegistration(t, goldenRoot)
}

func runVector(t *testing.T, c fiscaltest.GoldenCase) {
	t.Helper()

	got, err := evaluate(t, c)
	if c.ExpectError != "" {
		switch {
		case err == nil:
			t.Fatalf("expected an error containing %q, got %v", c.ExpectError, got)
		case !strings.Contains(err.Error(), c.ExpectError):
			t.Fatalf("error %q does not contain %q", err, c.ExpectError)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(c.Expect) {
		t.Fatalf("got %d results %v, vectors register %d %v", len(got), got, len(c.Expect), c.Expect)
	}
	for i := range got {
		if got[i] != c.Expect[i] {
			t.Fatalf("result %d: got %q, vectors register %q (whole result %v)", i, got[i], c.Expect[i], got)
		}
	}
}

func evaluate(t *testing.T, c fiscaltest.GoldenCase) ([]string, error) {
	t.Helper()

	switch c.Op {
	case "add", "sub", "mul":
		if c.HasPolicy() {
			t.Fatalf("%s is exact and takes no policy (ADR-0002 §2.1)", c.Op)
		}
		x, y := decimalInput(t, c, 0), decimalInput(t, c, 1)
		var out apd.Decimal
		var err error
		switch c.Op {
		case "add":
			err = fiscal.Add(&out, x, y)
		case "sub":
			err = fiscal.Sub(&out, x, y)
		case "mul":
			err = fiscal.Mul(&out, x, y)
		}
		return results(&out, err)
	}

	policy, err := policyOf(t, c)
	if err != nil {
		return nil, err
	}

	switch c.Op {
	case "round":
		var out apd.Decimal
		return results(&out, fiscal.ApplyPolicy(&out, decimalInput(t, c, 0), policy))
	case "quo":
		var out apd.Decimal
		return results(&out, fiscal.Quo(&out, decimalInput(t, c, 0), decimalInput(t, c, 1), policy))
	case "apply_rate":
		var out apd.Decimal
		return results(&out, fiscal.ApplyRate(&out, decimalInput(t, c, 0), decimalInput(t, c, 1), policy))
	case "allocate":
		if len(c.Inputs) == 0 {
			t.Fatal("allocate takes a total and its weights")
		}
		total := fiscaltest.Money(t, c.Inputs[0], vectorCurrency)
		weights := fiscaltest.Amounts(t, vectorCurrency, c.Inputs[1:]...)
		parts, err := fiscal.Allocate(total, weights, policy)
		if err != nil {
			return nil, err
		}
		return fiscaltest.Strings(parts), nil
	}

	t.Fatalf("unknown op %q", c.Op)
	return nil, nil
}

// policyOf decodes the case's policy through the production content path, so a
// malformed-policy vector asserts what a malformed bundle would meet.
func policyOf(t *testing.T, c fiscaltest.GoldenCase) (fiscal.RoundingPolicy, error) {
	t.Helper()
	if !c.HasPolicy() {
		t.Fatalf("%s requires a policy (ADR-0002 §2.2)", c.Op)
	}
	return fiscal.DecodeRoundingPolicy(c.PolicyContent())
}

// decimalInput parses a canonical decimal string.
func decimalInput(t *testing.T, c fiscaltest.GoldenCase, i int) *apd.Decimal {
	t.Helper()
	if i >= len(c.Inputs) {
		t.Fatalf("%s: input %d missing", c.Op, i)
	}
	d, cond, err := apd.NewFromString(c.Inputs[i])
	if err != nil {
		t.Fatalf("input %d %q: %v", i, c.Inputs[i], err)
	}
	// A case that expects a result must not smuggle a rounding step into its
	// own parse. A case that expects a refusal is entitled to feed the runtime
	// a value it cannot hold — that is what it is asserting.
	if c.ExpectError == "" && (cond.Inexact() || d.NumDigits() > fiscal.Precision) {
		t.Fatalf("input %d %q is not representable at precision %d", i, c.Inputs[i], fiscal.Precision)
	}
	return d
}

func results(out *apd.Decimal, err error) ([]string, error) {
	if err != nil {
		return nil, err
	}
	return []string{out.Text('f')}, nil
}
