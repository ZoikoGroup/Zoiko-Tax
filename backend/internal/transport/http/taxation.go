package http

import (
	"net/http"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/taxation"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The location-aware surface: a document in, a document determination out,
// and GET /v1/coverage. As in determination.go, what stays here is parsing
// the contract's strings and rendering results; nothing here selects,
// evaluates or rounds.

// ---------------------------------------------------------------------------
// ingress
// ---------------------------------------------------------------------------

// taxDocument reads the contract's document. Every amount is parsed in the
// document currency and in the contract's decimal form, so an amount in any
// other spelling is refused here rather than normalised into the evidence.
func taxDocument(in gen.TaxDocument) (*taxation.Document, error) {
	if !currencyForm.MatchString(in.Currency) {
		return nil, errs.Invalid("document.currency", errs.ReasonInvalidValue, "A currency is an ISO 4217 alphabetic code, such as EUR.")
	}
	cur := fiscal.Currency(in.Currency)
	money := func(field, s string) (fiscal.Money, error) {
		return parseMoney(field, gen.MoneyValue{Amount: s, Currency: in.Currency})
	}
	d := &taxation.Document{
		Type: string(in.Type), Currency: cur, PriceMode: string(in.PriceMode),
		Seller: taxParty(in.Seller), Customer: taxParty(in.Customer),
	}
	var err error
	if d.Evidence, err = situsEvidence("document.evidence", in.Evidence); err != nil {
		return nil, err
	}
	for _, c := range in.Certificates {
		cert := taxation.Certificate{
			ID: c.ID, Type: c.Type, Holder: c.Holder, Jurisdiction: c.Jurisdiction,
			TaxTypes: c.TaxTypes, Components: c.Components,
			ValidFrom: c.ValidFrom, Verification: string(c.Verification),
		}
		if c.ValidTo != nil {
			cert.ValidTo = *c.ValidTo
		}
		d.Certificates = append(d.Certificates, cert)
	}
	for _, l := range in.Lines {
		line := taxation.Line{Key: l.Key}
		if l.Sku != nil {
			line.SKU = *l.Sku
		}
		if l.Service != nil {
			line.Service = string(*l.Service)
		}
		if line.Amount, err = money("document.lines.amount", l.Amount); err != nil {
			return nil, err
		}
		if line.Quantity, err = quantity("document.lines.quantity", l.Quantity); err != nil {
			return nil, err
		}
		if line.Evidence, err = situsEvidence("document.lines.evidence", l.Evidence); err != nil {
			return nil, err
		}
		for _, p := range l.Parts {
			part := taxation.Part{Key: p.Key}
			if p.Service != nil {
				part.Service = string(*p.Service)
			}
			if part.Amount, err = money("document.lines.parts.amount", p.Amount); err != nil {
				return nil, err
			}
			if part.Quantity, err = quantity("document.lines.parts.quantity", p.Quantity); err != nil {
				return nil, err
			}
			line.Parts = append(line.Parts, part)
		}
		d.Lines = append(d.Lines, line)
	}
	return d, nil
}

func taxParty(p gen.TaxParty) taxation.Party {
	out := taxation.Party{ID: p.ID}
	if p.Establishment != nil {
		pl := place(*p.Establishment)
		out.Establishment = &pl
	}
	if p.Vat != nil {
		out.VAT = &taxation.VATIdentification{Country: p.Vat.Country, Validation: string(p.Vat.Validation)}
	}
	return out
}

func place(p gen.Place) taxation.Place {
	var out taxation.Place
	if p.Jurisdiction != nil {
		out.Jurisdiction = *p.Jurisdiction
	}
	if p.Subdivision != nil {
		out.Subdivision = *p.Subdivision
	}
	if p.Country != nil {
		out.Country = *p.Country
	}
	return out
}

func situsEvidence(field string, in []gen.SitusEvidenceItem) ([]taxation.Evidence, error) {
	var out []taxation.Evidence
	for _, e := range in {
		ev := taxation.Evidence{Type: string(e.Type), Source: string(e.Source), Place: place(e.Place)}
		if e.CollectedAt != nil {
			t, err := parseTimestamp(field+".collectedAt", *e.CollectedAt)
			if err != nil {
				return nil, err
			}
			ev.CollectedAt = t
		}
		out = append(out, ev)
	}
	return out, nil
}

func quantity(field string, q *gen.QuantityValue) (*fiscal.Quantity, error) {
	if q == nil {
		return nil, nil
	}
	if err := parseDecimal(field+".value", q.Value); err != nil {
		return nil, err
	}
	v, err := fiscal.ParseQuantity(q.Value, fiscal.Unit(q.Unit))
	if err != nil {
		return nil, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue,
			"The quantity cannot be represented at the runtime's precision.")
	}
	return &v, nil
}

// determinationRequest reads the either-or of a quote or commit body.
func determinationRequest(input *gen.DeterminationInput, document *gen.TaxDocument) (evidence.Input, *taxation.Document, error) {
	switch {
	case input != nil && document != nil:
		return evidence.Input{}, nil, errs.Invalid("input", errs.ReasonInvalidValue,
			"A determination carries named inputs or a document, not both.")
	case document != nil:
		doc, err := taxDocument(*document)
		return evidence.Input{}, doc, err
	case input != nil:
		in, err := determinationInput(*input)
		return in, nil, err
	}
	return evidence.Input{}, nil, errs.Invalid("input", errs.ReasonMissingField,
		"A determination carries named inputs or a document.")
}

// ---------------------------------------------------------------------------
// egress
// ---------------------------------------------------------------------------

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toDocumentDetermination(r *taxation.Result) *gen.DocumentDetermination {
	if r == nil {
		return nil
	}
	out := &gen.DocumentDetermination{
		State: gen.DeterminationState(r.State), ReasonCode: r.Reason,
		Currency: string(r.Currency), PriceMode: gen.PriceMode(r.PriceMode),
		ProfileVersion: r.ProfileVersion, GraphVersion: r.GraphVersion,
		Items: make([]gen.DeterminationItem, 0, len(r.Items)),
		Trace: make([]gen.DeterminationStep, 0, len(r.Trace)),
	}
	if t := r.Totals; t != nil {
		out.Totals = &gen.DocumentTotals{
			Net: t.Net.CanonicalString(), CustomerTax: t.CustomerTax.CanonicalString(),
			CustomerTotal: t.CustomerTotal.CanonicalString(), ProviderAccrual: t.ProviderAccrual.CanonicalString(),
			ReverseCharge: t.ReverseCharge.CanonicalString(),
		}
	}
	for _, it := range r.Items {
		item := gen.DeterminationItem{
			Key: it.Key, Line: it.Line, Part: optString(it.Part), Sku: optString(it.SKU),
			Amount: it.Amount.CanonicalString(), State: gen.DeterminationState(it.State), ReasonCode: optString(it.Reason),
			SitusOutcome: optString(it.SitusOutcome), SitusRule: optString(it.SitusRule), Applicable: it.Applicable,
			PlaceOfSupply: optString(it.PlaceOfSupply), CustomerRole: optString(it.CustomerRole),
			Supply: optString(it.Supply), Regime: optString(it.Regime), NeededEvidence: it.NeededEvidence,
		}
		if it.Service != "" {
			s := gen.ServiceFamily(it.Service)
			item.Service = &s
		}
		if it.Net != nil {
			n := it.Net.CanonicalString()
			item.Net = &n
		}
		for _, c := range it.Components {
			item.Components = append(item.Components, toComponentResult(c))
		}
		out.Items = append(out.Items, item)
	}
	for _, s := range r.Trace {
		out.Trace = append(out.Trace, gen.DeterminationStep{
			Item: optString(s.Item), Component: optString(s.Component), Step: s.Step, Detail: s.Detail,
		})
	}
	return out
}

func toComponentResult(c taxation.ComponentResult) gen.TaxComponentResult {
	r := c.Responsibility
	out := gen.TaxComponentResult{
		ID: c.ID, Version: c.Version, Regime: c.Regime, Jurisdiction: c.Jurisdiction, Authority: c.Authority,
		TaxType: c.TaxType, Name: c.Name, Treatment: gen.Treatment(c.Treatment), Inclusive: c.Inclusive,
		RateKind: c.RateKind, Rate: c.Rate, Unit: optString(c.Unit),
		Raw: c.Raw.CanonicalString(), Amount: c.Amount.CanonicalString(), Charged: c.Charged.CanonicalString(),
		LegalReason: optString(c.LegalReason), Source: c.Source, Citation: c.Citation,
		Responsibility: gen.ResponsibilityAssignment{
			ID: r.ID, Incidence: gen.ResponsibleParty(r.Incidence), Collection: gen.ResponsibleParty(r.Collection),
			Reporting: gen.ResponsibleParty(r.Reporting), Remittance: gen.ResponsibleParty(r.Remittance),
			EconomicBearer: gen.ResponsibleParty(r.EconomicBearer),
		},
	}
	if c.Base != nil {
		b := c.Base.CanonicalString()
		out.Base = &b
	}
	if e := c.Exemption; e != nil {
		ex := &gen.ExemptionApplied{Certificate: e.Certificate, Type: e.Type}
		if e.Assessed != nil {
			a := e.Assessed.CanonicalString()
			ex.Assessed = &a
		}
		out.Exemption = ex
	}
	return out
}

// ---------------------------------------------------------------------------
// GET /v1/coverage
// ---------------------------------------------------------------------------

func (rt *Router) handleCoverage(w http.ResponseWriter, r *http.Request) {
	var b *rule.Bundle
	if rt.Content != nil {
		b = rt.Content.Current()
	}
	if b == nil || b.Taxation() == nil {
		writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonNoContentBundle,
			"The cell has no active content bundle with location-aware content."))
		return
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	if q := r.URL.Query().Get("at"); q != "" {
		t, err := parseTimestamp("at", q)
		if err != nil {
			writeProblem(w, r, rt.log, err)
			return
		}
		at = t
	}
	engine := b.Taxation()
	out := gen.Coverage{
		Bundle:         gen.BundleRef{BundleID: b.ID(), Digest: b.Digest(), IrVersion: saturate32(b.IRVersion())},
		ProfileVersion: engine.Version(), GraphVersion: engine.GraphVersion(),
		At:      canonical.FormatTime(at),
		Regimes: []gen.CoverageRegime{},
	}
	for _, rg := range engine.Coverage(at) {
		reg := gen.CoverageRegime{
			ID: rg.ID, Country: rg.Country, Name: rg.Name, RequiresLevel: optString(rg.RequiresLevel),
			InclusivePricing: rg.InclusivePricing, Components: []gen.CoverageComponent{},
		}
		for _, s := range rg.Services {
			reg.Services = append(reg.Services, gen.ServiceFamily(s))
		}
		for _, c := range rg.Components {
			cc := gen.CoverageComponent{
				ID: c.ID, Jurisdiction: c.Jurisdiction, Authority: c.Authority, TaxType: c.TaxType, Name: c.Name,
				RateKind: c.RateKind, Rate: c.Rate, Currency: optString(c.Currency), Unit: optString(c.Unit),
				LegalReason: optString(c.LegalReason), From: c.From, To: optString(c.To),
				Source: c.Source, Citation: c.Citation,
			}
			for _, s := range c.Services {
				cc.Services = append(cc.Services, gen.ServiceFamily(s))
			}
			for _, t := range c.Treatments {
				cc.Treatments = append(cc.Treatments, gen.Treatment(t))
			}
			reg.Components = append(reg.Components, cc)
		}
		out.Regimes = append(out.Regimes, reg)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}
