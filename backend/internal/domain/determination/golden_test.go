package determination_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/determination"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/goldentest"
)

// The registered determination corpus (testdata/golden/determination), run
// with exact assertions. A case's schedule is decoded the way content is —
// rounding policies through fiscal.DecodeRoundingPolicy, decimals through
// fiscal.ParseMoney and ParseRate — so the corpus exercises the production
// ingress, not a test shortcut.

const determinationCorpus = "../../../testdata/golden/determination"

type goldenCase struct {
	ID          string         `json:"id"`
	Oracle      string         `json:"oracle"`
	Schedule    goldenSchedule `json:"schedule"`
	Currency    string         `json:"currency"`
	Lines       []goldenLine   `json:"lines"`
	Expect      *goldenExpect  `json:"expect"`
	ExpectError string         `json:"expectError"`
	Reqs        []string       `json:"requirements"`
}

type goldenSchedule struct {
	Components                []goldenComponent `json:"components"`
	ExtractionPolicy          json.RawMessage   `json:"extractionPolicy"`
	InclusiveSubtractionOrder []string          `json:"inclusiveSubtractionOrder"`
}

type goldenComponent struct {
	ID           string          `json:"id"`
	Jurisdiction string          `json:"jurisdiction"`
	TaxType      string          `json:"taxType"`
	Inclusive    bool            `json:"inclusive"`
	Base         goldenBase      `json:"base"`
	Rate         goldenRate      `json:"rate"`
	Policy       json.RawMessage `json:"policy"`
}

type goldenBase struct {
	Kind          string             `json:"kind"`
	Components    []string           `json:"components"`
	Adjustments   []goldenAdjustment `json:"adjustments"`
	AllowNegative bool               `json:"allowNegative"`
}

type goldenAdjustment struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Amount     string `json:"amount"`
	Proportion string `json:"proportion"`
}

type goldenRate struct {
	Kind       string `json:"kind"`
	Proportion string `json:"proportion"`
	Basis      string `json:"basis"`
	PerUnit    string `json:"perUnit"`
	Flat       string `json:"flat"`
	Bracket    *struct {
		Mode  string `json:"mode"`
		Tiers []struct {
			From string `json:"from"`
			Rate string `json:"rate"`
		} `json:"tiers"`
	} `json:"bracket"`
}

type goldenLine struct {
	Key        string   `json:"key"`
	Amount     string   `json:"amount"`
	Quantity   string   `json:"quantity"`
	Unit       string   `json:"unit"`
	Applicable []string `json:"applicable"`
}

type goldenExpect struct {
	Lines []struct {
		Key     string            `json:"key"`
		Net     string            `json:"net"`
		Amounts map[string]string `json:"amounts"`
	} `json:"lines"`
}

// TestGoldenDeterminationCorpus is tier 2 for DET-001: every registered case,
// exactly. There is no tolerance anywhere in this file.
func TestGoldenDeterminationCorpus(t *testing.T) {
	for _, set := range goldentest.Load(t, determinationCorpus) {
		t.Run(set.Set, func(t *testing.T) {
			for _, raw := range set.Cases {
				var c goldenCase
				dec := json.NewDecoder(bytes.NewReader(raw))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&c); err != nil {
					t.Fatalf("decode case: %v", err)
				}
				t.Run(c.ID, func(t *testing.T) { runGolden(t, c) })
			}
		})
	}
}

func runGolden(t *testing.T, c goldenCase) {
	cur := fiscal.Currency(c.Currency)
	sched, err := decodeSchedule(c.Schedule, cur)
	if err != nil {
		t.Fatalf("decode schedule: %v", err)
	}
	plan, err := determination.Compile(sched)
	if c.ExpectError != "" {
		if err == nil || !strings.Contains(err.Error(), c.ExpectError) {
			t.Fatalf("Compile: %v; want an error containing %q", err, c.ExpectError)
		}
		return
	}
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	doc := determination.Document{Currency: cur}
	for _, l := range c.Lines {
		amt, err := fiscal.ParseMoney(l.Amount, cur)
		if err != nil {
			t.Fatal(err)
		}
		dl := determination.Line{Key: l.Key, Amount: amt}
		for _, a := range l.Applicable {
			dl.Applicable = append(dl.Applicable, determination.ComponentID(a))
		}
		if l.Quantity != "" {
			q, err := fiscal.ParseQuantity(l.Quantity, fiscal.Unit(l.Unit))
			if err != nil {
				t.Fatal(err)
			}
			dl.Quantity = &q
		}
		doc.Lines = append(doc.Lines, dl)
	}
	got, err := plan.Evaluate(doc)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(got.Lines) != len(c.Expect.Lines) {
		t.Fatalf("%d lines, want %d", len(got.Lines), len(c.Expect.Lines))
	}
	for i, want := range c.Expect.Lines {
		l := got.Lines[i]
		if l.Key != want.Key || l.Net.CanonicalString() != want.Net {
			t.Errorf("line %d: key %s net %s, want %s %s", i, l.Key, l.Net.CanonicalString(), want.Key, want.Net)
		}
		amounts := map[string]string{}
		for _, comp := range l.Components {
			amounts[string(comp.Component)] = comp.Amount.CanonicalString()
		}
		if len(amounts) != len(want.Amounts) {
			t.Errorf("line %s: components %v, want %v", l.Key, amounts, want.Amounts)
		}
		for id, w := range want.Amounts {
			if amounts[id] != w {
				t.Errorf("line %s %s = %s, want %s", l.Key, id, amounts[id], w)
			}
		}
	}
}

func decodeSchedule(g goldenSchedule, cur fiscal.Currency) (determination.Schedule, error) {
	var s determination.Schedule
	if len(g.ExtractionPolicy) > 0 {
		p, err := fiscal.DecodeRoundingPolicy(g.ExtractionPolicy)
		if err != nil {
			return s, err
		}
		s.ExtractionPolicy = &p
	}
	for _, id := range g.InclusiveSubtractionOrder {
		s.InclusiveSubtractionOrder = append(s.InclusiveSubtractionOrder, determination.ComponentID(id))
	}
	for _, gc := range g.Components {
		p, err := fiscal.DecodeRoundingPolicy(gc.Policy)
		if err != nil {
			return s, err
		}
		c := determination.Component{
			ID: determination.ComponentID(gc.ID), JurisdictionID: gc.Jurisdiction, TaxType: gc.TaxType, Inclusive: gc.Inclusive,
			Policy: p, Base: determination.Base{Kind: determination.BaseKind(gc.Base.Kind), AllowNegative: gc.Base.AllowNegative},
			Rate: determination.Rate{Kind: determination.RateKind(gc.Rate.Kind)},
		}
		for _, ref := range gc.Base.Components {
			c.Base.Components = append(c.Base.Components, determination.ComponentID(ref))
		}
		for _, a := range gc.Base.Adjustments {
			adj := determination.BaseAdjustment{Kind: determination.BaseAdjustmentKind(a.Kind), Name: a.Name}
			if a.Amount != "" {
				m, err := fiscal.ParseMoney(a.Amount, cur)
				if err != nil {
					return s, err
				}
				adj.Amount = &m
			}
			if a.Proportion != "" {
				r, err := fiscal.ParseRate(a.Proportion, fiscal.RateBasisNet)
				if err != nil {
					return s, err
				}
				adj.Proportion = &r
			}
			c.Base.Adjustments = append(c.Base.Adjustments, adj)
		}
		r := gc.Rate
		if r.Proportion != "" {
			v, err := fiscal.ParseRate(r.Proportion, fiscal.RateBasis(r.Basis))
			if err != nil {
				return s, err
			}
			c.Rate.Proportion = &v
		}
		if r.PerUnit != "" {
			m, err := fiscal.ParseMoney(r.PerUnit, cur)
			if err != nil {
				return s, err
			}
			c.Rate.PerUnit = &m
		}
		if r.Flat != "" {
			m, err := fiscal.ParseMoney(r.Flat, cur)
			if err != nil {
				return s, err
			}
			c.Rate.Flat = &m
		}
		if r.Bracket != nil {
			b := &determination.Bracket{Mode: determination.BracketMode(r.Bracket.Mode)}
			for _, tier := range r.Bracket.Tiers {
				from, err := fiscal.ParseMoney(tier.From, cur)
				if err != nil {
					return s, err
				}
				rate, err := fiscal.ParseRate(tier.Rate, fiscal.RateBasisNet)
				if err != nil {
					return s, err
				}
				b.Tiers = append(b.Tiers, determination.Tier{From: from, Rate: rate})
			}
			c.Rate.Bracket = b
		}
		s.Components = append(s.Components, c)
	}
	return s, nil
}
