package taxation

import (
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Canonical renders the profile for the signed manifest (ADR-0011 §2.7),
// field by field. Array order is the author's: it is what the four-eyes
// review read, and a reviewer comparing two releases should see the same
// order the author wrote. A profile decoded from these bytes re-encodes to
// them, which is what the manifest decoder checks.
func (p Profile) Canonical() canonical.Value {
	js := make([]canonical.Value, len(p.Jurisdictions))
	for i, j := range p.Jurisdictions {
		js[i] = canonical.Object(
			canonical.F("id", canonical.String(j.ID)),
			canonical.F("parent", canonical.OptString(j.Parent)),
			canonical.F("level", canonical.String(j.Level)),
			canonical.F("name", canonical.String(j.Name)),
			canonical.F("country", canonical.String(j.Country)),
			canonical.F("code", canonical.OptString(j.Code)),
			canonical.F("timezone", canonical.String(j.Timezone)),
			canonical.F("effectiveFrom", canonical.String(j.EffectiveFrom)),
			canonical.F("effectiveTo", canonical.OptString(j.EffectiveTo)),
		)
	}
	ms := canonical.Absent()
	if len(p.Memberships) > 0 {
		vs := make([]canonical.Value, len(p.Memberships))
		for i, m := range p.Memberships {
			vs[i] = canonical.Object(
				canonical.F("special", canonical.String(m.Special)),
				canonical.F("members", stringArray(m.Members)),
			)
		}
		ms = canonical.Array(vs...)
	}
	rules := make([]canonical.Value, len(p.Situs.Rules))
	for i, r := range p.Situs.Rules {
		rel := make([]canonical.Field, 0, len(r.Reliability))
		for _, t := range sortedKeys(r.Reliability) {
			rel = append(rel, canonical.F(t, canonical.String(r.Reliability[t])))
		}
		required := canonical.Absent()
		if r.Required != 0 {
			required = canonical.Integer(int64(r.Required))
		}
		rules[i] = canonical.Object(
			canonical.F("id", canonical.String(r.ID)),
			canonical.F("service", canonical.OptString(r.Service)),
			canonical.F("role", canonical.OptString(r.Role)),
			canonical.F("mode", canonical.String(r.Mode)),
			canonical.F("reliability", canonical.Object(rel...)),
			canonical.F("order", stringArray(r.Order)),
			canonical.F("minimumReliability", canonical.OptString(r.MinimumReliability)),
			canonical.F("required", required),
			canonical.F("eligible", stringArray(r.Eligible)),
			canonical.F("agreementLevel", canonical.OptString(r.AgreementLevel)),
			canonical.F("roaming", canonical.OptString(r.Roaming)),
		)
	}
	resp := make([]canonical.Value, len(p.Responsibility))
	for i, r := range p.Responsibility {
		resp[i] = canonical.Object(
			canonical.F("id", canonical.String(r.ID)),
			canonical.F("incidence", canonical.String(r.Incidence)),
			canonical.F("collection", canonical.String(r.Collection)),
			canonical.F("reporting", canonical.String(r.Reporting)),
			canonical.F("remittance", canonical.String(r.Remittance)),
			canonical.F("economicBearer", canonical.String(r.EconomicBearer)),
		)
	}
	regimes := make([]canonical.Value, len(p.Regimes))
	for i, r := range p.Regimes {
		regimes[i] = canonical.Object(
			canonical.F("id", canonical.String(r.ID)),
			canonical.F("country", canonical.String(r.Country)),
			canonical.F("name", canonical.String(r.Name)),
			canonical.F("services", stringArray(r.Services)),
			canonical.F("requiresLevel", canonical.OptString(r.RequiresLevel)),
			canonical.F("extraction", policyValue(r.Extraction)),
			canonical.F("excludedSubdivisions", stringArray(r.ExcludedSubdivisions)),
			canonical.F("source", canonical.String(r.Source)),
			canonical.F("citation", canonical.String(r.Citation)),
		)
	}
	comps := make([]canonical.Value, len(p.Components))
	for i, c := range p.Components {
		applies := make([]canonical.Value, len(c.Applies))
		for j, a := range c.Applies {
			applies[j] = canonical.Object(
				canonical.F("roles", stringArray(a.Roles)),
				canonical.F("supply", canonical.String(a.Supply)),
				canonical.F("treatment", canonical.String(a.Treatment)),
				canonical.F("responsibility", canonical.String(a.Responsibility)),
			)
		}
		versions := make([]canonical.Value, len(c.Versions))
		for j, v := range c.Versions {
			versions[j] = canonical.Object(
				canonical.F("from", canonical.String(v.From)),
				canonical.F("to", canonical.OptString(v.To)),
				canonical.F("kind", canonical.String(v.Kind)),
				canonical.F("rate", canonical.OptString(v.Rate)),
				canonical.F("amount", canonical.OptString(v.Amount)),
				canonical.F("currency", canonical.OptString(v.Currency)),
				canonical.F("unit", canonical.OptString(v.Unit)),
				canonical.F("legalReason", canonical.OptString(v.LegalReason)),
				canonical.F("source", canonical.String(v.Source)),
				canonical.F("citation", canonical.String(v.Citation)),
			)
		}
		rounding := c.Rounding
		comps[i] = canonical.Object(
			canonical.F("id", canonical.String(c.ID)),
			canonical.F("regime", canonical.String(c.Regime)),
			canonical.F("jurisdiction", canonical.String(c.Jurisdiction)),
			canonical.F("authority", canonical.String(c.Authority)),
			canonical.F("taxType", canonical.String(c.TaxType)),
			canonical.F("name", canonical.String(c.Name)),
			canonical.F("emit", canonical.String(c.Emit)),
			canonical.F("services", stringArray(c.Services)),
			canonical.F("applies", canonical.Array(applies...)),
			canonical.F("base", canonical.Object(
				canonical.F("kind", canonical.String(c.Base.Kind)),
				canonical.F("components", stringArray(c.Base.Components)),
			)),
			canonical.F("rounding", policyValue(&rounding)),
			canonical.F("exemptBy", stringArray(c.ExemptBy)),
			canonical.F("versions", canonical.Array(versions...)),
		)
	}
	return canonical.Object(
		canonical.F("version", canonical.String(p.Version)),
		canonical.F("graphVersion", canonical.String(p.GraphVersion)),
		canonical.F("jurisdictions", canonical.Array(js...)),
		canonical.F("memberships", ms),
		canonical.F("situs", canonical.Object(
			canonical.F("undeterminedAs", canonical.String(p.Situs.UndeterminedAs)),
			canonical.F("rules", canonical.Array(rules...)),
		)),
		canonical.F("responsibility", canonical.Array(resp...)),
		canonical.F("regimes", canonical.Array(regimes...)),
		canonical.F("components", canonical.Array(comps...)),
	)
}

func policyValue(p *fiscal.RoundingPolicy) canonical.Value {
	if p == nil || p.Mode() == "" {
		return canonical.Absent()
	}
	return canonical.Object(
		canonical.F("mode", canonical.String(string(p.Mode()))),
		canonical.F("scale", canonical.Integer(int64(p.Scale()))),
		canonical.F("basis", canonical.String(string(p.Basis()))),
	)
}
