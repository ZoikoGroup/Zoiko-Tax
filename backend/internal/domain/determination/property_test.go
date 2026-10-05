package determination_test

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/determination"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// The DET-001 §14.2 properties, over generated inputs. rapid runs from a fixed
// seed unless one is passed, so a failure reproduces (ADR-0018 §2.12).

// decimal renders n * 10^-scale in plain canonical form.
func decimal(n int64, scale int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	digits := strconv.FormatInt(n, 10)
	if scale > 0 {
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		digits = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	}
	if neg {
		digits = "-" + digits
	}
	return digits
}

// inclusiveSchedule draws one to four inclusive proportional components, each
// either on net or compounding on the ones before it.
func inclusiveSchedule(t *rapid.T, tb testing.TB) (*determination.Plan, []determination.ComponentID) {
	n := rapid.IntRange(1, 4).Draw(t, "components")
	ext := fiscaltest.LinePolicy(tb, fiscal.RoundHalfEven, 2)
	var comps []determination.Component
	var ids []determination.ComponentID
	for i := 0; i < n; i++ {
		id := determination.ComponentID(fmt.Sprintf("c%d", i))
		rate := decimal(rapid.Int64Range(0, 3000).Draw(t, "rate"), 4) // 0 .. 0.3000
		r, err := fiscal.ParseRate(rate, fiscal.RateBasisNet)
		if err != nil {
			tb.Fatal(err)
		}
		c := determination.Component{
			ID: id, JurisdictionID: "jurisdiction:xx", TaxType: "T", Inclusive: true,
			Base:   determination.Base{Kind: determination.BaseLineNet},
			Rate:   determination.Rate{Kind: determination.RateAdValorem, Proportion: &r},
			Policy: fiscaltest.LinePolicy(tb, fiscal.RoundHalfUp, 2),
		}
		if i > 0 && rapid.Bool().Draw(t, "compounds") {
			kind := determination.BaseNetPlusComponents
			if rapid.Bool().Draw(t, "componentSum") {
				kind = determination.BaseComponentSum
			}
			c.Base = determination.Base{Kind: kind, Components: []determination.ComponentID{ids[rapid.IntRange(0, i-1).Draw(t, "on")]}}
		}
		comps = append(comps, c)
		ids = append(ids, id)
	}
	// At least one non-zero rate, or the extracted tax has nowhere to go.
	nonZero := false
	for _, c := range comps {
		if !fiscal.FactorOf(*c.Rate.Proportion).IsZero() {
			nonZero = true
		}
	}
	if !nonZero {
		r, _ := fiscal.ParseRate("0.1", fiscal.RateBasisNet)
		comps[0].Rate.Proportion = &r
	}
	p, err := determination.Compile(determination.Schedule{Components: comps, ExtractionPolicy: &ext})
	if err != nil {
		tb.Fatalf("generated schedule refused: %v", err)
	}
	return p, ids
}

func TestPropertyExtractionIsExact(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		p, ids := inclusiveSchedule(rt, t)
		gross := decimal(rapid.Int64Range(1, 10_000_000).Draw(rt, "gross"), 2)
		d, err := p.Evaluate(determination.Document{Currency: eur, Lines: []determination.Line{
			{Key: "1", Amount: fiscaltest.Money(t, gross, eur), Applicable: ids},
		}})
		if err != nil {
			rt.Fatal(err)
		}
		tax, err := d.Lines[0].Tax(eur)
		if err != nil {
			rt.Fatal(err)
		}
		sum, err := d.Lines[0].Net.Add(tax)
		if err != nil {
			rt.Fatal(err)
		}
		if c, _ := sum.Cmp(fiscaltest.Money(t, gross, eur)); c != 0 {
			rt.Fatalf("net %s + tax %s = %s != gross %s", d.Lines[0].Net, tax, sum, gross)
		}
	})
}

func TestPropertyDocumentAllocationIsTotal(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		vat := adValorem(t, "vat", decimal(rapid.Int64Range(1, 3000).Draw(rt, "rate"), 4))
		vat.Policy = fiscaltest.Policy(t, fiscal.RoundHalfUp, 2, fiscal.BasisDocument)
		p, err := determination.Compile(determination.Schedule{Components: []determination.Component{vat}})
		if err != nil {
			rt.Fatal(err)
		}
		n := rapid.IntRange(1, 12).Draw(rt, "lines")
		var lines []determination.Line
		raw := money(t, "0")
		for i := 0; i < n; i++ {
			amt := fiscaltest.Money(t, decimal(rapid.Int64Range(0, 1_000_000).Draw(rt, "net"), 2), eur)
			lines = append(lines, determination.Line{Key: strconv.Itoa(i), Amount: amt, Applicable: all("vat")})
			r, _ := amt.Times(*vat.Rate.Proportion)
			raw, _ = raw.Add(r)
		}
		d, err := p.Evaluate(determination.Document{Currency: eur, Lines: lines})
		if err != nil {
			rt.Fatal(err)
		}
		sum := money(t, "0")
		for _, l := range d.Lines {
			sum, _ = sum.Add(l.Components[0].Amount)
		}
		want, _ := raw.Round(vat.Policy)
		if c, _ := sum.Cmp(want); c != 0 {
			rt.Fatalf("lines sum to %s; the rounded document total is %s", sum, want)
		}
	})
}

func TestPropertyReversalClosesAndEvaluationIsIdempotent(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		p, ids := inclusiveSchedule(rt, t)
		gross := decimal(rapid.Int64Range(1, 10_000_000).Draw(rt, "gross"), 2)
		doc := determination.Document{Currency: eur, Lines: []determination.Line{
			{Key: "1", Amount: fiscaltest.Money(t, gross, eur), Applicable: ids},
		}}
		d1, err := p.Evaluate(doc)
		if err != nil {
			rt.Fatal(err)
		}
		d2, err := p.Evaluate(doc)
		if err != nil {
			rt.Fatal(err)
		}
		b1, _ := canonical.Encode(d1.Canonical())
		b2, _ := canonical.Encode(d2.Canonical())
		if !bytes.Equal(b1, b2) {
			rt.Fatal("re-determining identical input gave a different decision")
		}
		pos, err := determination.NetPosition(d1, determination.Reverse(d1))
		if err != nil {
			rt.Fatal(err)
		}
		for k, v := range pos {
			if !v.IsZero() {
				rt.Fatalf("%+v nets to %s after reversal", k, v)
			}
		}
	})
}
