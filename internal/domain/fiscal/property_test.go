package fiscal_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/cockroachdb/apd/v3"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
	"pgregory.net/rapid"
)

// Tier 1 of ADR-0018 §2.1, on the invariants ADR-0002 §5.1 control 4 names:
// allocation totality, rounding idempotence, and mode-by-mode boundary
// behaviour. The vectors assert particular answers; these assert that the
// answers cannot be wrong in a particular way, over inputs nobody chose.
//
// rapid runs from a fixed seed unless one is passed on the command line, so a
// failure here reproduces (ADR-0018 §2.12).

const propertyCurrency fiscal.Currency = "XTS"

var allModes = []fiscal.RoundingMode{
	fiscal.RoundHalfUp, fiscal.RoundHalfEven, fiscal.RoundHalfDown,
	fiscal.RoundDown, fiscal.RoundUp, fiscal.RoundCeiling, fiscal.RoundFloor,
}

// symmetricModes are the modes defined by distance from zero rather than by
// direction on the number line. CEILING and FLOOR are the two that are not:
// they round a value and its negation the same way round the number line, so
// a debit and its credit can materialise at different magnitudes under them.
// That is what those modes mean, not a defect, which is why the symmetry
// property is stated over the other five.
var symmetricModes = []fiscal.RoundingMode{
	fiscal.RoundHalfUp, fiscal.RoundHalfEven, fiscal.RoundHalfDown,
	fiscal.RoundDown, fiscal.RoundUp,
}

func TestAllocationIsTotal(t *testing.T) {
	// ADR-0002 §2.6 and ADR-0018 §2.2 (balance): the parts sum to the whole.
	// Not approximately, and not for the inputs someone thought of.
	rapid.Check(t, func(rt *rapid.T) {
		total, weights, policy := drawAllocation(t, rt, allModes)

		parts, err := fiscal.Allocate(total, weights, policy)
		if err != nil {
			rt.Fatalf("allocate %s over %v: %v", total, fiscaltest.Strings(weights), err)
		}
		if len(parts) != len(weights) {
			rt.Fatalf("got %d parts for %d weights", len(parts), len(weights))
		}

		rounded, err := total.Round(policy)
		if err != nil {
			rt.Fatalf("round total: %v", err)
		}
		sum := sumOf(rt, parts)
		if sum.Cmp(decimalOf(rounded)) != 0 {
			rt.Fatalf("parts %v sum to %s, total materialises at %s (%s)",
				fiscaltest.Strings(parts), sum.Text('f'), rounded, policy)
		}
	})
}

func TestAllocationIsDeterministic(t *testing.T) {
	// ADR-0011: the same canonical input replays to the same bytes. An
	// allocation that depended on map order or on sort instability would be
	// correct on the invoice and divergent on the replay.
	rapid.Check(t, func(rt *rapid.T) {
		total, weights, policy := drawAllocation(t, rt, allModes)

		first, err := fiscal.Allocate(total, weights, policy)
		if err != nil {
			rt.Fatalf("allocate: %v", err)
		}
		for i := 0; i < 4; i++ {
			again, err := fiscal.Allocate(total, weights, policy)
			if err != nil {
				rt.Fatalf("allocate again: %v", err)
			}
			if got, want := strings.Join(fiscaltest.Strings(again), ","), strings.Join(fiscaltest.Strings(first), ","); got != want {
				rt.Fatalf("run %d gave %s, first run gave %s", i+2, got, want)
			}
		}
	})
}

func TestAllocationIsSymmetricAboutZero(t *testing.T) {
	// A credit note must reverse the document it corrects line by line. That
	// holds only if allocating the negation negates the allocation.
	rapid.Check(t, func(rt *rapid.T) {
		magnitude, weights, policy := drawAllocation(t, rt, symmetricModes)
		positive := fiscaltest.Money(t, strings.TrimPrefix(magnitude.String(), "-"), propertyCurrency)
		negative := fiscaltest.Money(t, "-"+strings.TrimPrefix(magnitude.String(), "-"), propertyCurrency)

		debit, err := fiscal.Allocate(positive, weights, policy)
		if err != nil {
			rt.Fatalf("allocate debit: %v", err)
		}
		credit, err := fiscal.Allocate(negative, weights, policy)
		if err != nil {
			rt.Fatalf("allocate credit: %v", err)
		}
		for i := range debit {
			var netted apd.Decimal
			if err := fiscal.Add(&netted, decimalOf(debit[i]), decimalOf(credit[i])); err != nil {
				rt.Fatalf("net line %d: %v", i, err)
			}
			if !netted.IsZero() {
				rt.Fatalf("line %d: %s and %s do not net to zero (%s)", i, debit[i], credit[i], netted.Text('f'))
			}
		}
	})
}

func TestEqualWeightsAllocateWithinOneMinorUnit(t *testing.T) {
	// Largest-remainder distributes the residual one unit at a time, so equal
	// lines cannot diverge by more than a single minor unit however many of
	// them there are. Round-then-adjust-the-last-line fails this (§3.4).
	rapid.Check(t, func(rt *rapid.T) {
		scale := rapid.Int32Range(0, 6).Draw(rt, "scale")
		mode := allModes[rapid.IntRange(0, len(allModes)-1).Draw(rt, "mode")]
		policy := fiscaltest.Policy(t, mode, scale, fiscal.BasisDocument)
		total := fiscaltest.Money(t, drawAmount(rt, "total", 0), propertyCurrency)
		lines := rapid.IntRange(1, 12).Draw(rt, "lines")

		parts, err := fiscal.AllocateEvenly(total, lines, policy)
		if err != nil {
			rt.Fatalf("allocate evenly: %v", err)
		}

		lowest, highest := decimalOf(parts[0]), decimalOf(parts[0])
		for _, part := range parts {
			value := decimalOf(part)
			if value.Cmp(lowest) < 0 {
				lowest = value
			}
			if value.Cmp(highest) > 0 {
				highest = value
			}
		}
		var spread apd.Decimal
		if err := fiscal.Sub(&spread, highest, lowest); err != nil {
			rt.Fatalf("spread: %v", err)
		}
		if spread.Cmp(apd.New(1, -scale)) > 0 {
			rt.Fatalf("parts %v span %s, more than one minor unit at scale %d",
				fiscaltest.Strings(parts), spread.Text('f'), scale)
		}
	})
}

func TestRoundingIsIdempotent(t *testing.T) {
	// ADR-0002 §5.1 control 4. A materialisation step that is not idempotent
	// makes the number of times a value crosses a boundary fiscally
	// significant, and nothing in the evidence record would record that count.
	rapid.Check(t, func(rt *rapid.T) {
		scale := rapid.Int32Range(0, fiscal.MaxScale).Draw(rt, "scale")
		mode := allModes[rapid.IntRange(0, len(allModes)-1).Draw(rt, "mode")]
		policy := fiscaltest.Policy(t, mode, scale, fiscal.BasisLine)
		value := decimal(t, drawAmount(rt, "value", 0))

		var once, twice apd.Decimal
		if err := fiscal.ApplyPolicy(&once, value, policy); err != nil {
			rt.Fatalf("round once: %v", err)
		}
		if err := fiscal.ApplyPolicy(&twice, &once, policy); err != nil {
			rt.Fatalf("round twice: %v", err)
		}
		if once.Text('f') != twice.Text('f') {
			rt.Fatalf("%s rounds to %s and then to %s under %s",
				value.Text('f'), once.Text('f'), twice.Text('f'), policy)
		}
	})
}

func TestExactOperationsAreReversible(t *testing.T) {
	// The claim behind ADR-0002 §2.1: within 34 digits, addition and
	// subtraction lose nothing. Equality is numeric rather than textual,
	// because the result takes the finer of the two exponents — 1.5 + 2.25 -
	// 2.25 is 1.50, the same number carrying a scale it did not start with.
	rapid.Check(t, func(rt *rapid.T) {
		a := decimal(t, drawAmount(rt, "a", 0))
		b := decimal(t, drawAmount(rt, "b", 0))

		var sum, back apd.Decimal
		if err := fiscal.Add(&sum, a, b); err != nil {
			rt.Fatalf("add %s and %s: %v", a.Text('f'), b.Text('f'), err)
		}
		if err := fiscal.Sub(&back, &sum, b); err != nil {
			rt.Fatalf("subtract: %v", err)
		}
		if back.Cmp(a) != 0 {
			rt.Fatalf("%s + %s - %s = %s", a.Text('f'), b.Text('f'), b.Text('f'), back.Text('f'))
		}
	})
}

// drawAllocation generates a total, a set of non-negative weights that is not
// entirely zero, and a policy. The weights are kept allocatable on purpose: the
// refusals have their own tests, and a generator that spent its runs on inputs
// the function rejects would assert nothing about the ones it accepts.
func drawAllocation(t *testing.T, rt *rapid.T, modes []fiscal.RoundingMode) (fiscal.Money, []fiscal.Money, fiscal.RoundingPolicy) {
	t.Helper()

	scale := rapid.Int32Range(0, 6).Draw(rt, "scale")
	mode := modes[rapid.IntRange(0, len(modes)-1).Draw(rt, "mode")]
	policy := fiscaltest.Policy(t, mode, scale, fiscal.BasisDocument)

	total := fiscaltest.Money(t, drawAmount(rt, "total", 0), propertyCurrency)

	lines := rapid.IntRange(1, 10).Draw(rt, "lines")
	weights := make([]fiscal.Money, lines)
	for i := range weights {
		weights[i] = fiscaltest.Money(t, drawAmount(rt, "weight"+strconv.Itoa(i), 1), propertyCurrency)
	}
	// At least one weight carries proportion, so the allocation has an answer.
	weights[rapid.IntRange(0, lines-1).Draw(rt, "carrier")] = fiscaltest.Money(t, "1", propertyCurrency)
	return total, weights, policy
}

// drawAmount generates a canonical decimal string. sign 0 allows negatives;
// sign 1 keeps the value non-negative, which is what a weight must be.
func drawAmount(rt *rapid.T, label string, sign int) string {
	units := rapid.Int64Range(0, 1_000_000_000_000).Draw(rt, label+"-units")
	scale := rapid.Int32Range(0, 6).Draw(rt, label+"-scale")
	negative := sign == 0 && rapid.Bool().Draw(rt, label+"-negative")

	digits := strconv.FormatInt(units, 10)
	for int32(len(digits)) <= scale {
		digits = "0" + digits
	}
	amount := digits
	if scale > 0 {
		split := int32(len(digits)) - scale
		amount = digits[:split] + "." + digits[split:]
	}
	if negative {
		amount = "-" + amount
	}
	return amount
}

func sumOf(rt *rapid.T, parts []fiscal.Money) *apd.Decimal {
	var sum apd.Decimal
	for _, part := range parts {
		if err := fiscal.Add(&sum, &sum, decimalOf(part)); err != nil {
			rt.Fatalf("sum parts: %v", err)
		}
	}
	return &sum
}

func decimalOf(m fiscal.Money) *apd.Decimal {
	value := m.Decimal()
	return &value
}
