package compile_test

import (
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/content/compile"
	"github.com/zoikogroup/zoikotax/backend/internal/content/dsl"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/taxation"
	"github.com/zoikogroup/zoikotax/backend/internal/goldentest"
)

const (
	globalSource  = "../../../../content/packs/global-telecom/global-telecom.ztax"
	globalProfile = "../../../../content/packs/global-telecom/taxation.json"
	// usRatesAsOf is the date the us-state-equipment-sales-tax rule copied its
	// rates from taxation.json. The rule has no effective dating, so a rate
	// change there is a new rule version here, and this date moves with it.
	usRatesAsOf = "2026-10-30"
)

func globalBundle(t *testing.T) *rule.Bundle {
	t.Helper()
	prog, err := dsl.Parse("global-telecom.ztax", string(goldentest.ReadFile(t, globalSource)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m, err := compile.Compile(prog)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	m.Digest = "zt1:0000000000000000000000000000000000000000000000000000000000000000"
	b, err := rule.Load(m)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return b
}

func usStateEquipment(t *testing.T, b *rule.Bundle, state string, net fiscal.Money) (fiscal.Money, bool) {
	t.Helper()
	got, err := rule.Evaluate(b, rule.Frame{
		Money:   map[string]fiscal.Money{"line.netAmount": net},
		Strings: map[string]string{"line.usState": state},
	})
	if err != nil {
		t.Fatalf("%s: evaluate: %v", state, err)
	}
	return got.Emitted["US_STATE_EQUIPMENT_SALES_TAX"].Money, got.Emitted["US_STATE_COVERED"].Bool
}

// TestUSStateRuleMatchesTaxationProfile holds the rule graph to the
// location-aware content it was copied from: every state's equipment sales
// tax, as taxation.json has it in force on usRatesAsOf, is the rate the rule
// applies. Two copies of a rate that disagree are a liability nobody can
// explain, so a change to either fails here until the other follows.
func TestUSStateRuleMatchesTaxationProfile(t *testing.T) {
	b := globalBundle(t)
	p, err := taxation.DecodeProfile(goldentest.ReadFile(t, globalProfile))
	if err != nil {
		t.Fatal(err)
	}
	// A net amount large enough that every rate, at up to five decimals,
	// lands exactly on a cent, so the comparison is free of rounding.
	net, err := fiscal.ParseMoney("100000.00", "USD")
	if err != nil {
		t.Fatal(err)
	}

	states := 0
	for _, c := range p.Components {
		if c.Regime != "regime:us-state-taxes" || !strings.HasSuffix(c.ID, ".sales-goods") {
			continue
		}
		state := strings.ToUpper(strings.TrimPrefix(c.Jurisdiction, "jurisdiction:us-"))
		var rate string
		for _, v := range c.Versions {
			if v.From <= usRatesAsOf && (v.To == "" || v.To > usRatesAsOf) {
				rate = v.Rate
			}
		}
		if rate == "" {
			t.Errorf("%s: %s has no version in force on %s", state, c.ID, usRatesAsOf)
			continue
		}
		r, err := fiscal.ParseRate(rate, fiscal.RateBasisNet)
		if err != nil {
			t.Fatalf("%s: %v", state, err)
		}
		want, err := net.Times(r)
		if err != nil {
			t.Fatal(err)
		}

		got, covered := usStateEquipment(t, b, state, net)
		if !covered {
			t.Errorf("%s: US_STATE_COVERED is false", state)
		}
		if cmp, err := got.Cmp(want); err != nil || cmp != 0 {
			t.Errorf("%s: rule applies %s to %s, taxation.json says %s (%s)", state, got, net, want, rate)
		}
		states++
	}
	if states != 51 {
		t.Errorf("checked %d states, want the fifty and DC", states)
	}
}

// TestUSStateRuleDoesNotCoverOtherCodes: a code that is no state cannot be
// refused in IR version 1, so the rule says so beside a zero amount.
func TestUSStateRuleDoesNotCoverOtherCodes(t *testing.T) {
	b := globalBundle(t)
	net, err := fiscal.ParseMoney("100.00", "USD")
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"ZZ", "ca", "PR", ""} {
		got, covered := usStateEquipment(t, b, code, net)
		if covered || !got.IsZero() {
			t.Errorf("%q: covered=%v amount=%s, want false and zero", code, covered, got)
		}
	}
}

// TestUSStateRuleRounds: a rate that does not land on a cent rounds half up
// at the line, as the components in taxation.json do.
func TestUSStateRuleRounds(t *testing.T) {
	b := globalBundle(t)
	net, err := fiscal.ParseMoney("19.99", "USD")
	if err != nil {
		t.Fatal(err)
	}
	// 19.99 × 0.06875 = 1.3743125 → 1.37 (Minnesota).
	if got, _ := usStateEquipment(t, b, "MN", net); got.CanonicalString() != "1.37" {
		t.Errorf("MN: %s, want 1.37", got.CanonicalString())
	}
	// 19.99 × 0.0725 = 1.449275 → 1.45 (California).
	if got, _ := usStateEquipment(t, b, "CA", net); got.CanonicalString() != "1.45" {
		t.Errorf("CA: %s, want 1.45", got.CanonicalString())
	}
}
