package fx_test

import (
	"errors"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fx"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

var march = time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)

func table(t *testing.T) *fx.RateTable {
	r := fx.Rate{Base: "USD", Quote: "EUR", Value: fiscaltest.Rate(t, "0.9183", fiscal.RateBasisNet),
		Source: "central-bank:xa", Date: march, Method: fx.MethodMonthly, Version: "2027-03"}
	tbl, err := fx.NewRateTable([]fx.Rate{r})
	if err != nil {
		t.Fatal(err)
	}
	return tbl
}

func policy(t *testing.T) fx.Policy {
	return fx.Policy{Source: "central-bank:xa", Method: fx.MethodMonthly, Rounding: fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 2)}
}

func TestFINREQ0028ConversionStoresSourceRateDateAndMethod(t *testing.T) {
	c, err := fx.Convert(fiscaltest.Money(t, "100.00", "USD"), "EUR", march.AddDate(0, 0, 3), table(t), policy(t))
	if err != nil {
		t.Fatal(err)
	}
	// 100.00 * 0.9183 = 91.83.
	if c.Converted.String() != "91.83" || c.Original.String() != "100.00" || c.Original.Currency() != "USD" {
		t.Fatalf("converted %s from %s %s", c.Converted, c.Original, c.Original.Currency())
	}
	if c.Rate.Source == "" || c.Rate.Date.IsZero() || c.Rate.Method != fx.MethodMonthly || c.Rate.Version != "2027-03" {
		t.Fatalf("rate evidence %+v", c.Rate)
	}
}

func TestFINREQ0103MissingRateIsExplicitNeverGuessed(t *testing.T) {
	_, err := fx.Convert(fiscaltest.Money(t, "100.00", "GBP"), "EUR", march, table(t), policy(t))
	if !errors.Is(err, fx.ErrRateMissing) {
		t.Fatalf("a missing rate gave %v", err)
	}
	// A rate dated after the conversion date is not used either.
	_, err = fx.Convert(fiscaltest.Money(t, "100.00", "USD"), "EUR", march.AddDate(0, 0, -1), table(t), policy(t))
	if !errors.Is(err, fx.ErrRateMissing) {
		t.Fatalf("a future rate was used: %v", err)
	}
}

func TestFINREQ0030RateSourceIsChosenByPolicy(t *testing.T) {
	p := policy(t)
	p.Source = "provider:other"
	if _, err := fx.Convert(fiscaltest.Money(t, "100.00", "USD"), "EUR", march, table(t), p); !errors.Is(err, fx.ErrRateMissing) {
		t.Fatalf("a rate from another source was used: %v", err)
	}
}
