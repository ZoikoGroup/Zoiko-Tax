package fiscal_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

func TestParseMoneyPreservesScale(t *testing.T) {
	// ADR-0011 §2.2: scale is semantic. "45.00" and "45" are the same number
	// and different fiscal statements, and canonicalization has to see the
	// difference, so parsing must not normalise it away.
	for _, amount := range []string{"45", "45.0", "45.00", "45.000"} {
		if got := fiscaltest.Money(t, amount, "EUR").String(); got != amount {
			t.Errorf("parsed %q and rendered %q", amount, got)
		}
	}
}

func TestParseMoneyRejections(t *testing.T) {
	cases := map[string]struct{ amount, currency string }{
		"empty currency":     {"10.00", ""},
		"not a number":       {"ten euros", "EUR"},
		"two decimal points": {"1.2.3", "EUR"},
		"empty amount":       {"", "EUR"},
		// 35 significant digits against a context of 34. A value the runtime
		// cannot hold exactly must not enter it rounded.
		"beyond precision": {"1.0000000000000000000000000000000001", "EUR"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if m, err := fiscal.ParseMoney(c.amount, fiscal.Currency(c.currency)); err == nil {
				t.Fatalf("accepted %q %q as %s", c.amount, c.currency, m)
			}
		})
	}
}

func TestMoneyArithmeticRefusesMixedCurrencies(t *testing.T) {
	euros := fiscaltest.Money(t, "10.00", "EUR")
	dollars := fiscaltest.Money(t, "10.00", "USD")

	if _, err := euros.Add(dollars); !errors.Is(err, fiscal.ErrCurrencyMismatch) {
		t.Errorf("Add: got %v, want a currency mismatch", err)
	}
	if _, err := euros.Sub(dollars); !errors.Is(err, fiscal.ErrCurrencyMismatch) {
		t.Errorf("Sub: got %v, want a currency mismatch", err)
	}
}

func TestMoneyApplyRateKeepsTheCurrency(t *testing.T) {
	net := fiscaltest.Money(t, "19.99", "EUR")
	rate := fiscaltest.Rate(t, "0.0875", fiscal.RateBasisNet)

	tax, err := net.ApplyRate(rate, fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 2))
	if err != nil {
		t.Fatalf("apply rate: %v", err)
	}
	if got := tax.String(); got != "1.75" {
		t.Errorf("got %s, want 1.75", got)
	}
	if tax.Currency() != "EUR" {
		t.Errorf("currency %s, want EUR", tax.Currency())
	}

	gross, err := net.Add(tax)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if got := gross.String(); got != "21.74" {
		t.Errorf("gross: got %s, want 21.74", got)
	}
}

func TestParseRateRequiresAKnownBasis(t *testing.T) {
	// A bare decimal is not a rate: 0.06 means nothing until it is known
	// whether it applies to a net amount, a gross amount or a quantity.
	if r, err := fiscal.ParseRate("0.06", "NETT"); err == nil {
		t.Fatalf("accepted an unknown basis as %s", r.Basis())
	}
	for _, basis := range []fiscal.RateBasis{
		fiscal.RateBasisNet, fiscal.RateBasisGross, fiscal.RateBasisPerUnit, fiscal.RateBasisCompound,
	} {
		if got := fiscaltest.Rate(t, "0.06", basis).Basis(); got != basis {
			t.Errorf("basis: got %s, want %s", got, basis)
		}
	}
}

func TestMoneyRoundIsTheOnlyMaterialisation(t *testing.T) {
	// ADR-0002 §2.5: a Money reaches its minor-unit scale when it is written
	// out, not between two rule steps. Until then it carries what it was given.
	intermediate := fiscaltest.Money(t, "1.749125", "EUR")
	if got := intermediate.String(); got != "1.749125" {
		t.Fatalf("an intermediate was rounded on the way in: %s", got)
	}

	written, err := intermediate.Round(fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 2))
	if err != nil {
		t.Fatalf("round: %v", err)
	}
	if got := written.String(); got != "1.75" {
		t.Fatalf("got %s, want 1.75", got)
	}
	if got := intermediate.String(); got != "1.749125" {
		t.Fatalf("Round mutated its receiver: %s", got)
	}
}

func TestNoFiscalTypeReturnsAFloat(t *testing.T) {
	// ADR-0001 control 1 and ADR-0002 §2.8. The fiscalfloat analyzer enforces
	// this across the module, including the routes that reach around the type —
	// a conversion in a log field, a DTO holding both kinds of number. This
	// test states the narrower claim about the exported surface itself, so that
	// a Float64() added to Money fails here too, without waiting for the
	// analyzer to be wired into the pipeline.
	for _, value := range []any{fiscal.Money{}, fiscal.Rate{}, fiscal.RoundingPolicy{}} {
		typ := reflect.TypeOf(value)
		for i := 0; i < typ.NumMethod(); i++ {
			method := typ.Method(i)
			for j := 0; j < method.Type.NumOut(); j++ {
				switch method.Type.Out(j).Kind() {
				case reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
					t.Errorf("%s.%s returns %s", typ.Name(), method.Name, method.Type.Out(j))
				}
			}
		}
	}
}
