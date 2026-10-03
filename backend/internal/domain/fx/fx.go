// Package fx is the foreign-exchange evidence of ZTAX-FIN-001 §8: every
// conversion that affects tax, reporting or remittance records the rate, its
// source, its date, its method and its version (ZTAX-FIN-REQ-0028), and keeps
// the original amount beside the converted one.
//
// The package is pure. It converts against a RateTable the caller supplies —
// a snapshot pinned at the time of the original decision — and never fetches
// a rate, so an exact replay cannot reach a live feed (ZTAX-FIN-REQ-0029).
// Which source and which reporting currency apply is content or
// configuration by legal context, carried as a Policy (ZTAX-FIN-REQ-0030).
package fx

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
)

// ErrRateMissing is a required rate that is not in the table. It is an
// explicit PENDING state for the caller, never a guess at a nearby rate
// (ZTAX-FIN-REQ-0103).
var ErrRateMissing = errors.New("fx: required rate is missing")

// Method is how a rate was set.
type Method string

// The methods.
const (
	MethodSpot          Method = "SPOT"
	MethodMonthly       Method = "MONTHLY"
	MethodCustom        Method = "CUSTOM"
	MethodAuthorityRule Method = "AUTHORITY_RULE"
)

// Rate is one exact decimal rate for a currency pair: one unit of Base is
// Value units of Quote.
type Rate struct {
	Base    fiscal.Currency
	Quote   fiscal.Currency
	Value   fiscal.Rate
	Source  string
	Date    time.Time
	Method  Method
	Version string
}

// Validate refuses a rate missing any of the evidence a conversion records.
func (r Rate) Validate() error {
	switch {
	case r.Base == "" || r.Quote == "" || r.Base == r.Quote:
		return fmt.Errorf("fx: rate pair %s/%s", r.Base, r.Quote)
	case r.Source == "" || r.Version == "":
		return fmt.Errorf("fx: %s/%s rate has no source or no version", r.Base, r.Quote)
	case r.Date.IsZero():
		return fmt.Errorf("fx: %s/%s rate has no date", r.Base, r.Quote)
	}
	switch r.Method {
	case MethodSpot, MethodMonthly, MethodCustom, MethodAuthorityRule:
	default:
		return fmt.Errorf("fx: %s/%s rate has method %q", r.Base, r.Quote, r.Method)
	}
	if fiscal.FactorOf(r.Value).Sign() <= 0 {
		return fmt.Errorf("fx: %s/%s rate is not positive", r.Base, r.Quote)
	}
	return nil
}

// Policy is the legal context's FX choice: whose rates, by what method, on
// which date rule.
type Policy struct {
	Source string
	Method Method
	// Rounding is the conversion's rounding, from content.
	Rounding fiscal.RoundingPolicy
}

// RateTable is a pinned snapshot of rates.
type RateTable struct {
	rates []Rate
}

// NewRateTable validates a snapshot. Two rates for one pair, source, method
// and date are refused: the table must answer one way.
func NewRateTable(rates []Rate) (*RateTable, error) {
	seen := map[string]bool{}
	out := make([]Rate, len(rates))
	for i, r := range rates {
		if err := r.Validate(); err != nil {
			return nil, err
		}
		k := fmt.Sprintf("%s/%s|%s|%s|%s", r.Base, r.Quote, r.Source, r.Method, r.Date.UTC().Format(time.RFC3339))
		if seen[k] {
			return nil, fmt.Errorf("fx: two %s rates for %s", r.Method, k)
		}
		seen[k] = true
		out[i] = r
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return &RateTable{rates: out}, nil
}

// Find returns the rate a policy selects for a pair on a date: the latest rate
// of the policy's source and method dated on or before it.
func (t *RateTable) Find(base, quote fiscal.Currency, on time.Time, p Policy) (Rate, error) {
	var found *Rate
	for i := range t.rates {
		r := t.rates[i]
		if r.Base == base && r.Quote == quote && r.Source == p.Source && r.Method == p.Method && !r.Date.After(on) {
			found = &t.rates[i]
		}
	}
	if found == nil {
		return Rate{}, fmt.Errorf("%w: %s/%s from %s by %s on %s", ErrRateMissing, base, quote, p.Source, p.Method, on.Format(time.DateOnly))
	}
	return *found, nil
}

// Conversion is one recorded conversion: the original, the result and the
// rate evidence. The original amount is never replaced by the converted one.
type Conversion struct {
	Original  fiscal.Money
	Converted fiscal.Money
	Rate      Rate
	Rounding  fiscal.RoundingPolicy
}

// Convert converts an amount on a date under a policy.
func Convert(m fiscal.Money, to fiscal.Currency, on time.Time, table *RateTable, p Policy) (Conversion, error) {
	if m.Currency() == to {
		return Conversion{}, fmt.Errorf("fx: %s is already in %s", m.CanonicalString(), to)
	}
	r, err := table.Find(m.Currency(), to, on, p)
	if err != nil {
		return Conversion{}, err
	}
	out, err := m.ConvertAt(r.Value, to, p.Rounding)
	if err != nil {
		return Conversion{}, err
	}
	return Conversion{Original: m, Converted: out, Rate: r, Rounding: p.Rounding}, nil
}
