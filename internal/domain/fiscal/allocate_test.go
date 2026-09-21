package fiscal_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

// The arithmetic of allocation — which line receives which residual unit — is
// asserted by testdata/golden/decimal/allocation.json and by the totality
// property. This file covers what the vectors cannot: the refusals, and the
// even-split helper.

func TestAllocateEvenly(t *testing.T) {
	policy := fiscaltest.Policy(t, fiscal.RoundHalfUp, 2, fiscal.BasisDocument)
	parts, err := fiscal.AllocateEvenly(fiscaltest.Money(t, "0.05", "EUR"), 3, policy)
	if err != nil {
		t.Fatalf("allocate evenly: %v", err)
	}
	if got, want := strings.Join(fiscaltest.Strings(parts), ","), "0.02,0.02,0.01"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	for _, part := range parts {
		if part.Currency() != "EUR" {
			t.Errorf("part currency %s, want EUR", part.Currency())
		}
	}
}

func TestAllocateEvenlyRejectsNonPositivePartCounts(t *testing.T) {
	policy := fiscaltest.Policy(t, fiscal.RoundHalfUp, 2, fiscal.BasisDocument)
	for _, n := range []int{0, -1} {
		if parts, err := fiscal.AllocateEvenly(fiscaltest.Money(t, "1.00", "EUR"), n, policy); err == nil {
			t.Errorf("%d parts returned %v", n, fiscaltest.Strings(parts))
		}
	}
}

func TestAllocateRejectsMixedCurrencies(t *testing.T) {
	// A weight in another currency is not a weight, it is a conversion someone
	// forgot to perform. ADR-0002 keeps FX out of this package entirely.
	policy := fiscaltest.Policy(t, fiscal.RoundHalfUp, 2, fiscal.BasisDocument)
	weights := []fiscal.Money{
		fiscaltest.Money(t, "1.00", "EUR"),
		fiscaltest.Money(t, "1.00", "USD"),
	}
	_, err := fiscal.Allocate(fiscaltest.Money(t, "10.00", "EUR"), weights, policy)
	if !errors.Is(err, fiscal.ErrCurrencyMismatch) {
		t.Fatalf("got %v, want a currency mismatch", err)
	}
}

func TestAllocateRejectsAnUnallocatableTotal(t *testing.T) {
	policy := fiscaltest.Policy(t, fiscal.RoundHalfUp, 2, fiscal.BasisDocument)
	weights := fiscaltest.Amounts(t, "EUR", "0", "0")
	_, err := fiscal.Allocate(fiscaltest.Money(t, "1.00", "EUR"), weights, policy)
	if !errors.Is(err, fiscal.ErrNoWeight) {
		t.Fatalf("got %v, want ErrNoWeight", err)
	}
}

func TestAllocateAtTheScaleTheContentNames(t *testing.T) {
	// The same total and weights, allocated at two scales, are two different
	// legal answers — and each sums to its own materialisation of the total.
	// Scale is content, not a display concern (ADR-0002 §3.3).
	total := fiscaltest.Money(t, "1.00", "XTS")
	weights := fiscaltest.Amounts(t, "XTS", "1", "1", "1")

	for _, c := range []struct {
		scale int32
		want  string
	}{
		{scale: 2, want: "0.34,0.33,0.33"},
		{scale: 4, want: "0.3334,0.3333,0.3333"},
		{scale: 0, want: "1,0,0"},
	} {
		parts, err := fiscal.Allocate(total, weights, fiscaltest.Policy(t, fiscal.RoundHalfUp, c.scale, fiscal.BasisDocument))
		if err != nil {
			t.Fatalf("scale %d: %v", c.scale, err)
		}
		if got := strings.Join(fiscaltest.Strings(parts), ","); got != c.want {
			t.Errorf("scale %d: got %s, want %s", c.scale, got, c.want)
		}
	}
}
