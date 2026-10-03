package fiscal_test

import (
	"errors"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

func TestTimesIsExact(t *testing.T) {
	m := fiscaltest.Money(t, "10.05", "EUR")
	r := fiscaltest.Rate(t, "0.075", fiscal.RateBasisNet)
	got, err := m.Times(r)
	if err != nil {
		t.Fatal(err)
	}
	// 10.05 * 0.075 = 0.75375 — five places, no rounding.
	if got.String() != "0.75375" {
		t.Fatalf("Times = %s, want 0.75375", got)
	}
}

func TestDivFactorRoundsOnceUnderThePolicy(t *testing.T) {
	g := fiscaltest.Money(t, "100.00", "EUR")
	m := fiscal.FactorOne()
	vat, err := m.Add(fiscal.FactorOf(fiscaltest.Rate(t, "0.21", fiscal.RateBasisNet)))
	if err != nil {
		t.Fatal(err)
	}
	n, err := g.DivFactor(vat, fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 2))
	if err != nil {
		t.Fatal(err)
	}
	// 100 / 1.21 = 82.6446... -> 82.64
	if n.String() != "82.64" {
		t.Fatalf("DivFactor = %s, want 82.64", n)
	}
}

func TestDivFactorRefusesZero(t *testing.T) {
	g := fiscaltest.Money(t, "1.00", "EUR")
	var zero fiscal.Factor
	if _, err := g.DivFactor(zero, fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 2)); !errors.Is(err, fiscal.ErrDivisionByZero) {
		t.Fatalf("DivFactor by zero = %v, want ErrDivisionByZero", err)
	}
}

func TestCmpMinMaxRefuseMixedCurrencies(t *testing.T) {
	a := fiscaltest.Money(t, "1", "EUR")
	b := fiscaltest.Money(t, "1", "USD")
	if _, err := a.Cmp(b); !errors.Is(err, fiscal.ErrCurrencyMismatch) {
		t.Fatalf("Cmp across currencies = %v", err)
	}
	c := fiscaltest.Money(t, "2.5", "EUR")
	lo, _ := a.Min(c)
	hi, _ := a.Max(c)
	if lo.String() != "1" || hi.String() != "2.5" {
		t.Fatalf("Min/Max = %s/%s", lo, hi)
	}
}

func TestNegAndZeroKeepScale(t *testing.T) {
	m := fiscaltest.Money(t, "12.30", "EUR")
	if m.Neg().String() != "-12.30" {
		t.Fatalf("Neg = %s", m.Neg())
	}
	if m.Zero().CanonicalString() != "0.00" {
		t.Fatalf("Zero = %s, want 0.00", m.Zero().CanonicalString())
	}
}

func TestAllocateByFactorsIsTotal(t *testing.T) {
	total := fiscaltest.Money(t, "17.36", "EUR")
	w := []fiscal.Factor{
		fiscal.FactorOf(fiscaltest.Rate(t, "0.1", fiscal.RateBasisNet)),
		fiscal.FactorOf(fiscaltest.Rate(t, "0.11", fiscal.RateBasisNet)),
	}
	parts, err := fiscal.AllocateByFactors(total, w, fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 2))
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := parts[0].Add(parts[1])
	if c, _ := sum.Cmp(total); c != 0 {
		t.Fatalf("parts %v sum to %s, want %s", fiscaltest.Strings(parts), sum, total)
	}
}
