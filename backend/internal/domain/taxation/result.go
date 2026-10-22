package taxation

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Result is a document's determination: its state, its lines' states and
// components, the totals that must stay apart, and the trace.
type Result struct {
	State string
	// Reason is the registered reason code: NOT_AUTHORITATIVE for a resolved
	// document before A4, and the least resolved line's reason otherwise.
	Reason    string
	Currency  fiscal.Currency
	PriceMode string
	// ProfileVersion and GraphVersion name the taxation content and the
	// jurisdiction graph, beside the bundle digest the envelope pins.
	ProfileVersion string
	GraphVersion   string
	// Totals is present only for a RESOLVED document. An unresolved one has
	// no totals rather than zero ones.
	Totals *Totals
	Items  []ItemResult
	// Emitted is the slot totals a fiscal profile posts and assesses. Empty
	// unless RESOLVED, so an unresolved decision has no fiscal effect.
	Emitted map[string]fiscal.Money
	Trace   []Step
}

// Totals are the figures playbook §7 requires to remain different: what the
// customer is charged, what the provider bears, and what the customer
// accounts for itself.
type Totals struct {
	Net fiscal.Money
	// CustomerTax is every CUSTOMER_CHARGE amount: the customer-recoverable
	// charges on the invoice.
	CustomerTax fiscal.Money
	// CustomerTotal is Net plus CustomerTax.
	CustomerTotal fiscal.Money
	// ProviderAccrual is every PROVIDER_COST amount: borne by the seller,
	// never on the invoice.
	ProviderAccrual fiscal.Money
	// ReverseCharge is every REVERSE_CHARGE amount: self-assessed by the
	// customer, disclosed and neither charged nor remitted by the seller.
	ReverseCharge fiscal.Money
}

// ItemResult is one line's, or one bundle part's, determination.
type ItemResult struct {
	Key     string
	Line    string
	Part    string
	SKU     string
	Service string
	Amount  fiscal.Money
	State   string
	Reason  string
	// The situs decision, summarised; Situs is its full canonical record.
	SitusOutcome   string
	SitusRule      string
	Applicable     []string
	PlaceOfSupply  string
	CustomerRole   string
	Supply         string
	Regime         string
	NeededEvidence []string
	Situs          []byte
	// Net is the item's net after any inclusive extraction; RESOLVED only.
	Net        *fiscal.Money
	Components []ComponentResult
}

// ComponentResult is one charge on one item: its legal base, rule, rate,
// jurisdiction, roles, recoverability, rounding and source (playbook §7).
type ComponentResult struct {
	ID           string
	Version      string
	Regime       string
	Jurisdiction string
	Authority    string
	TaxType      string
	Name         string
	Emit         string
	Treatment    string
	Inclusive    bool
	RateKind     string
	Rate         string
	Unit         string
	Base         *fiscal.Money
	Raw          fiscal.Money
	Amount       fiscal.Money
	// Charged is what the customer pays for this component: Amount for a
	// CUSTOMER_CHARGE, zero for every other treatment.
	Charged        fiscal.Money
	LegalReason    string
	Responsibility ResponsibilityDecl
	Exemption      *Exemption
	Source         string
	Citation       string
}

// Exemption records a charge a certificate suppressed, and what it would
// have been: the assessment is retained, not discarded (playbook §29).
type Exemption struct {
	Certificate string
	Type        string
	Assessed    *fiscal.Money
}

// Step is one step of the trace.
type Step struct {
	Item      string
	Component string
	Step      string
	Detail    string
}

// Canonical renders the result for the evidence record.
func (r Result) Canonical() canonical.Value {
	totals := canonical.Absent()
	if t := r.Totals; t != nil {
		totals = canonical.Object(
			canonical.F("net", canonical.Money(t.Net)),
			canonical.F("customerTax", canonical.Money(t.CustomerTax)),
			canonical.F("customerTotal", canonical.Money(t.CustomerTotal)),
			canonical.F("providerAccrual", canonical.Money(t.ProviderAccrual)),
			canonical.F("reverseCharge", canonical.Money(t.ReverseCharge)),
		)
	}
	items := make([]canonical.Value, len(r.Items))
	for i, it := range r.Items {
		items[i] = it.canonical()
	}
	trace := make([]canonical.Value, len(r.Trace))
	for i, s := range r.Trace {
		trace[i] = canonical.Object(
			canonical.F("item", canonical.OptString(s.Item)),
			canonical.F("component", canonical.OptString(s.Component)),
			canonical.F("step", canonical.String(s.Step)),
			canonical.F("detail", canonical.String(s.Detail)),
		)
	}
	emitted := make([]canonical.Field, 0, len(r.Emitted))
	for _, slot := range sortedKeys(r.Emitted) {
		emitted = append(emitted, canonical.F(slot, canonical.Money(r.Emitted[slot])))
	}
	emittedValue := canonical.Absent()
	if len(emitted) > 0 {
		emittedValue = canonical.Object(emitted...)
	}
	return canonical.Object(
		canonical.F("state", canonical.String(r.State)),
		canonical.F("reason", canonical.String(r.Reason)),
		canonical.F("currency", canonical.String(string(r.Currency))),
		canonical.F("priceMode", canonical.String(r.PriceMode)),
		canonical.F("profileVersion", canonical.String(r.ProfileVersion)),
		canonical.F("graphVersion", canonical.String(r.GraphVersion)),
		canonical.F("totals", totals),
		canonical.F("items", canonical.Array(items...)),
		canonical.F("emitted", emittedValue),
		canonical.F("trace", canonical.Array(trace...)),
	)
}

func (it ItemResult) canonical() canonical.Value {
	net := canonical.Absent()
	if it.Net != nil {
		net = canonical.Money(*it.Net)
	}
	situs := canonical.Absent()
	if len(it.Situs) > 0 {
		situs = canonical.Raw(it.Situs)
	}
	comps := canonical.Absent()
	if len(it.Components) > 0 {
		cs := make([]canonical.Value, len(it.Components))
		for i, c := range it.Components {
			cs[i] = c.canonical()
		}
		comps = canonical.Array(cs...)
	}
	return canonical.Object(
		canonical.F("key", canonical.String(it.Key)),
		canonical.F("line", canonical.String(it.Line)),
		canonical.F("part", canonical.OptString(it.Part)),
		canonical.F("sku", canonical.OptString(it.SKU)),
		canonical.F("service", canonical.OptString(it.Service)),
		canonical.F("amount", canonical.Money(it.Amount)),
		canonical.F("state", canonical.String(it.State)),
		canonical.F("reason", canonical.OptString(it.Reason)),
		canonical.F("situsOutcome", canonical.OptString(it.SitusOutcome)),
		canonical.F("situsRule", canonical.OptString(it.SitusRule)),
		canonical.F("applicable", stringArray(it.Applicable)),
		canonical.F("placeOfSupply", canonical.OptString(it.PlaceOfSupply)),
		canonical.F("customerRole", canonical.OptString(it.CustomerRole)),
		canonical.F("supply", canonical.OptString(it.Supply)),
		canonical.F("regime", canonical.OptString(it.Regime)),
		canonical.F("neededEvidence", stringArray(it.NeededEvidence)),
		canonical.F("situs", situs),
		canonical.F("net", net),
		canonical.F("components", comps),
	)
}

func (c ComponentResult) canonical() canonical.Value {
	base := canonical.Absent()
	if c.Base != nil {
		base = canonical.Money(*c.Base)
	}
	exemption := canonical.Absent()
	if e := c.Exemption; e != nil {
		assessed := canonical.Absent()
		if e.Assessed != nil {
			assessed = canonical.Money(*e.Assessed)
		}
		exemption = canonical.Object(
			canonical.F("certificate", canonical.String(e.Certificate)),
			canonical.F("type", canonical.String(e.Type)),
			canonical.F("assessed", assessed),
		)
	}
	r := c.Responsibility
	return canonical.Object(
		canonical.F("id", canonical.String(c.ID)),
		canonical.F("version", canonical.String(c.Version)),
		canonical.F("regime", canonical.String(c.Regime)),
		canonical.F("jurisdiction", canonical.String(c.Jurisdiction)),
		canonical.F("authority", canonical.String(c.Authority)),
		canonical.F("taxType", canonical.String(c.TaxType)),
		canonical.F("name", canonical.String(c.Name)),
		canonical.F("emit", canonical.String(c.Emit)),
		canonical.F("treatment", canonical.String(c.Treatment)),
		canonical.F("inclusive", canonical.Bool(c.Inclusive)),
		canonical.F("rateKind", canonical.String(c.RateKind)),
		canonical.F("rate", canonical.String(c.Rate)),
		canonical.F("unit", canonical.OptString(c.Unit)),
		canonical.F("base", base),
		canonical.F("raw", canonical.Money(c.Raw)),
		canonical.F("amount", canonical.Money(c.Amount)),
		canonical.F("charged", canonical.Money(c.Charged)),
		canonical.F("legalReason", canonical.OptString(c.LegalReason)),
		canonical.F("responsibility", canonical.Object(
			canonical.F("id", canonical.String(r.ID)),
			canonical.F("incidence", canonical.String(r.Incidence)),
			canonical.F("collection", canonical.String(r.Collection)),
			canonical.F("reporting", canonical.String(r.Reporting)),
			canonical.F("remittance", canonical.String(r.Remittance)),
			canonical.F("economicBearer", canonical.String(r.EconomicBearer)),
		)),
		canonical.F("exemption", exemption),
		canonical.F("source", canonical.String(c.Source)),
		canonical.F("citation", canonical.String(c.Citation)),
	)
}

// DecodeResult reads a result from its canonical form, strictly, with the
// same round-trip check DecodeDocument makes.
func DecodeResult(data []byte) (Result, error) {
	var w resultWire
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return Result{}, fmt.Errorf("taxation: decode result: %w", err)
	}
	r, err := w.decode()
	if err != nil {
		return Result{}, err
	}
	again, err := canonical.Encode(r.Canonical())
	if err != nil {
		return Result{}, err
	}
	if !bytes.Equal(again, data) {
		return Result{}, fmt.Errorf("taxation: result is not in %s form", canonical.ProfileVersion)
	}
	return r, nil
}

type resultWire struct {
	State          string            `json:"state"`
	Reason         string            `json:"reason"`
	Currency       string            `json:"currency"`
	PriceMode      string            `json:"priceMode"`
	ProfileVersion string            `json:"profileVersion"`
	GraphVersion   string            `json:"graphVersion"`
	Totals         *totalsWire       `json:"totals"`
	Items          []itemWire        `json:"items"`
	Emitted        map[string]string `json:"emitted"`
	Trace          []struct {
		Item      string `json:"item"`
		Component string `json:"component"`
		Step      string `json:"step"`
		Detail    string `json:"detail"`
	} `json:"trace"`
}

type totalsWire struct {
	Net             string `json:"net"`
	CustomerTax     string `json:"customerTax"`
	CustomerTotal   string `json:"customerTotal"`
	ProviderAccrual string `json:"providerAccrual"`
	ReverseCharge   string `json:"reverseCharge"`
}

type itemWire struct {
	Key            string          `json:"key"`
	Line           string          `json:"line"`
	Part           string          `json:"part"`
	SKU            string          `json:"sku"`
	Service        string          `json:"service"`
	Amount         string          `json:"amount"`
	State          string          `json:"state"`
	Reason         string          `json:"reason"`
	SitusOutcome   string          `json:"situsOutcome"`
	SitusRule      string          `json:"situsRule"`
	Applicable     []string        `json:"applicable"`
	PlaceOfSupply  string          `json:"placeOfSupply"`
	CustomerRole   string          `json:"customerRole"`
	Supply         string          `json:"supply"`
	Regime         string          `json:"regime"`
	NeededEvidence []string        `json:"neededEvidence"`
	Situs          json.RawMessage `json:"situs"`
	Net            *string         `json:"net"`
	Components     []componentWire `json:"components"`
}

type componentWire struct {
	ID             string             `json:"id"`
	Version        string             `json:"version"`
	Regime         string             `json:"regime"`
	Jurisdiction   string             `json:"jurisdiction"`
	Authority      string             `json:"authority"`
	TaxType        string             `json:"taxType"`
	Name           string             `json:"name"`
	Emit           string             `json:"emit"`
	Treatment      string             `json:"treatment"`
	Inclusive      bool               `json:"inclusive"`
	RateKind       string             `json:"rateKind"`
	Rate           string             `json:"rate"`
	Unit           string             `json:"unit"`
	Base           *string            `json:"base"`
	Raw            string             `json:"raw"`
	Amount         string             `json:"amount"`
	Charged        string             `json:"charged"`
	LegalReason    string             `json:"legalReason"`
	Responsibility ResponsibilityDecl `json:"responsibility"`
	Exemption      *struct {
		Certificate string  `json:"certificate"`
		Type        string  `json:"type"`
		Assessed    *string `json:"assessed"`
	} `json:"exemption"`
	Source   string `json:"source"`
	Citation string `json:"citation"`
}

func (w resultWire) decode() (Result, error) {
	cur := fiscal.Currency(w.Currency)
	money := func(s string) (fiscal.Money, error) { return fiscal.ParseMoney(s, cur) }
	optMoney := func(s *string) (*fiscal.Money, error) {
		if s == nil {
			return nil, nil
		}
		m, err := money(*s)
		return &m, err
	}
	r := Result{
		State: w.State, Reason: w.Reason, Currency: cur, PriceMode: w.PriceMode,
		ProfileVersion: w.ProfileVersion, GraphVersion: w.GraphVersion,
	}
	if t := w.Totals; t != nil {
		var tt Totals
		var err error
		for _, f := range []struct {
			dst *fiscal.Money
			src string
		}{{&tt.Net, t.Net}, {&tt.CustomerTax, t.CustomerTax}, {&tt.CustomerTotal, t.CustomerTotal},
			{&tt.ProviderAccrual, t.ProviderAccrual}, {&tt.ReverseCharge, t.ReverseCharge}} {
			if *f.dst, err = money(f.src); err != nil {
				return Result{}, fmt.Errorf("taxation: result totals: %w", err)
			}
		}
		r.Totals = &tt
	}
	if len(w.Emitted) > 0 {
		r.Emitted = make(map[string]fiscal.Money, len(w.Emitted))
		for slot, s := range w.Emitted {
			m, err := money(s)
			if err != nil {
				return Result{}, fmt.Errorf("taxation: result emitted %s: %w", slot, err)
			}
			r.Emitted[slot] = m
		}
	}
	for _, iw := range w.Items {
		it := ItemResult{
			Key: iw.Key, Line: iw.Line, Part: iw.Part, SKU: iw.SKU, Service: iw.Service, State: iw.State, Reason: iw.Reason,
			SitusOutcome: iw.SitusOutcome, SitusRule: iw.SitusRule, Applicable: iw.Applicable,
			PlaceOfSupply: iw.PlaceOfSupply, CustomerRole: iw.CustomerRole, Supply: iw.Supply, Regime: iw.Regime,
			NeededEvidence: iw.NeededEvidence, Situs: []byte(iw.Situs),
		}
		var err error
		if it.Amount, err = money(iw.Amount); err != nil {
			return Result{}, fmt.Errorf("taxation: result item %s: %w", iw.Key, err)
		}
		if it.Net, err = optMoney(iw.Net); err != nil {
			return Result{}, fmt.Errorf("taxation: result item %s net: %w", iw.Key, err)
		}
		for _, cw := range iw.Components {
			c := ComponentResult{
				ID: cw.ID, Version: cw.Version, Regime: cw.Regime, Jurisdiction: cw.Jurisdiction, Authority: cw.Authority,
				TaxType: cw.TaxType, Name: cw.Name, Emit: cw.Emit, Treatment: cw.Treatment, Inclusive: cw.Inclusive,
				RateKind: cw.RateKind, Rate: cw.Rate, Unit: cw.Unit, LegalReason: cw.LegalReason,
				Responsibility: cw.Responsibility, Source: cw.Source, Citation: cw.Citation,
			}
			if c.Base, err = optMoney(cw.Base); err != nil {
				return Result{}, err
			}
			for _, f := range []struct {
				dst *fiscal.Money
				src string
			}{{&c.Raw, cw.Raw}, {&c.Amount, cw.Amount}, {&c.Charged, cw.Charged}} {
				if *f.dst, err = money(f.src); err != nil {
					return Result{}, fmt.Errorf("taxation: result component %s: %w", cw.ID, err)
				}
			}
			if e := cw.Exemption; e != nil {
				ex := Exemption{Certificate: e.Certificate, Type: e.Type}
				if ex.Assessed, err = optMoney(e.Assessed); err != nil {
					return Result{}, err
				}
				c.Exemption = &ex
			}
			it.Components = append(it.Components, c)
		}
		r.Items = append(r.Items, it)
	}
	for _, s := range w.Trace {
		r.Trace = append(r.Trace, Step{Item: s.Item, Component: s.Component, Step: s.Step, Detail: s.Detail})
	}
	return r, nil
}
