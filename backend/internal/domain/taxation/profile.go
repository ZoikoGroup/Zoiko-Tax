// Package taxation is location-aware determination: given a telecom document
// and where it was supplied, which taxes, fees and contributions apply, at
// what rates, who owes and performs each one, and what the customer is
// charged.
//
// It is the orchestration ZTAX-DET-001 §2.1 describes and nothing below it
// already did. Every stage is an existing package:
//
//  2. Resolve   jurisdiction            internal/domain/jurisdiction
//  3. Classify  each line               internal/domain/ontology (families)
//  4. Select    the applicable components            (this package)
//  6. Evaluate  the component DAG       internal/domain/determination
//  7. Round and allocate                internal/domain/determination
//  8. Emit      the decision and its trace           (this package)
//
// No rate, jurisdiction, threshold or responsibility rule is Go. They arrive
// as a Profile — the taxation section of a signed content bundle — and the
// package holds no default for any of them. What Go does decide is the
// shape: that every line must end in an explicit state, that a covered line
// with no applicable content is UNSUPPORTED rather than zero, that an
// exemption never suppresses a charge on a certificate nobody verified.
//
// The package is pure: no clock, no I/O, no randomness. The same Engine and
// Request give byte-identical results on every replay (ADR-0005 §2.4).
package taxation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/determination"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/jurisdiction"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ontology"
)

// Profile is the taxation section of a content bundle, as authored
// (content/packs/<pack>/taxation.json) and as signed into the manifest.
//
// Every decimal is a string and every instant or date is a string, so the
// canonical form carries no number a reader could reinterpret (ADR-0011 P1).
type Profile struct {
	// Version is the profile's own content version, recorded on every
	// decision alongside the bundle digest.
	Version string `json:"version"`
	// GraphVersion names the jurisdiction graph the profile declares.
	GraphVersion  string             `json:"graphVersion"`
	Jurisdictions []JurisdictionDecl `json:"jurisdictions"`
	Memberships   []MembershipDecl   `json:"memberships,omitempty"`
	Situs         SitusDecl          `json:"situs"`
	// Responsibility names reusable five-role assignments that treatments
	// refer to, so a pack states "seller collects and remits" once.
	Responsibility []ResponsibilityDecl `json:"responsibility"`
	Regimes        []RegimeDecl         `json:"regimes"`
	Components     []ComponentDecl      `json:"components"`
}

// JurisdictionDecl is one node of the jurisdiction graph (JUR-001 §2).
type JurisdictionDecl struct {
	ID     string `json:"id"`
	Parent string `json:"parent,omitempty"`
	Level  string `json:"level"`
	Name   string `json:"name"`
	// Country is ISO 3166-1 alpha-2.
	Country string `json:"country"`
	// Code is the ISO 3166-2 subdivision code ("CA-QC") a caller may name
	// the jurisdiction by. Countries are named by Country.
	Code     string `json:"code,omitempty"`
	Timezone string `json:"timezone"`
	// EffectiveFrom and EffectiveTo are calendar dates (YYYY-MM-DD) in the
	// jurisdiction's own timezone; EffectiveTo is exclusive.
	EffectiveFrom string `json:"effectiveFrom"`
	EffectiveTo   string `json:"effectiveTo,omitempty"`
}

// MembershipDecl lists the general jurisdictions a SPECIAL one claims.
type MembershipDecl struct {
	Special string   `json:"special"`
	Members []string `json:"members"`
}

// SitusDecl is the pack's situs rule set (JUR-001 §4).
type SitusDecl struct {
	// UndeterminedAs is how a customer whose role the evidence does not
	// settle is treated. Required, never assumed (JUR-REQ-0020).
	UndeterminedAs string          `json:"undeterminedAs"`
	Rules          []SitusRuleDecl `json:"rules"`
}

// SitusRuleDecl is one situs rule and the selection it binds to.
type SitusRuleDecl struct {
	ID string `json:"id"`
	// Service selects by ontology family; empty matches every family.
	Service string `json:"service,omitempty"`
	// Role selects by customer role; empty matches every role.
	Role        string            `json:"role,omitempty"`
	Mode        string            `json:"mode"`
	Reliability map[string]string `json:"reliability"`
	// PRECEDENCE.
	Order              []string `json:"order,omitempty"`
	MinimumReliability string   `json:"minimumReliability,omitempty"`
	// QUORUM.
	Required       int      `json:"required,omitempty"`
	Eligible       []string `json:"eligible,omitempty"`
	AgreementLevel string   `json:"agreementLevel,omitempty"`
	// Roaming is HOME, VISITED or BOTH; empty means the rule does not cover
	// roaming and a roaming transaction is UNSUPPORTED (JUR-REQ-0018).
	Roaming string `json:"roaming,omitempty"`
}

// ResponsibilityDecl is a named five-role assignment (ZTAX-OBL-REQ-0002 to
// -0006). Each role is SELLER, BUYER or NONE.
type ResponsibilityDecl struct {
	ID             string `json:"id"`
	Incidence      string `json:"incidence"`
	Collection     string `json:"collection"`
	Reporting      string `json:"reporting"`
	Remittance     string `json:"remittance"`
	EconomicBearer string `json:"economicBearer"`
}

// RegimeDecl is one country's coverage: which services the pack has content
// for, how deep a resolution must go before the country's taxes are known,
// and how inclusive prices are extracted.
type RegimeDecl struct {
	ID      string `json:"id"`
	Country string `json:"country"`
	Name    string `json:"name"`
	// Services is every ontology family the pack declares content for in
	// this country. A line of any other family is UNSUPPORTED here.
	Services []string `json:"services"`
	// RequiresLevel is the shallowest level a place of supply must resolve
	// to: STATE where provinces levy their own tax and a country-level
	// answer would silently omit it. Empty is COUNTRY.
	RequiresLevel string `json:"requiresLevel,omitempty"`
	// Extraction is the rounding of N := G / M for inclusive prices
	// (DET-001 §7.4). Absent: the regime does not support inclusive pricing.
	Extraction *fiscal.RoundingPolicy `json:"extraction,omitempty"`
	// ExcludedSubdivisions are ISO 3166-2 codes inside the country that are
	// outside this regime's territory — the Canary Islands for Spanish VAT.
	// A location there is UNSUPPORTED, never taxed under the national rules
	// that a subdivision the graph does not know would otherwise fall to.
	ExcludedSubdivisions []string `json:"excludedSubdivisions,omitempty"`
	Source               string   `json:"source"`
	Citation             string   `json:"citation"`
}

// ComponentDecl is one tax, fee or contribution in one jurisdiction.
type ComponentDecl struct {
	ID           string `json:"id"`
	Regime       string `json:"regime"`
	Jurisdiction string `json:"jurisdiction"`
	Authority    string `json:"authority"`
	TaxType      string `json:"taxType"`
	Name         string `json:"name"`
	// Emit is the result slot the component's document total is emitted
	// under, which a fiscal profile's postings and obligations name.
	Emit string `json:"emit"`
	// Services restricts the component to these families; empty is every
	// family the regime covers.
	Services []string `json:"services,omitempty"`
	// Applies lists the conditions under which the component applies and
	// the treatment each gives. At most one may match a line; a line no
	// entry matches is one the component does not apply to.
	Applies []ApplyDecl `json:"applies"`
	Base    BaseDecl    `json:"base"`
	// Rounding is required; there is no default (DET-REQ-0008).
	Rounding fiscal.RoundingPolicy `json:"rounding"`
	// ExemptBy lists the certificate types that may exempt the component.
	ExemptBy []string `json:"exemptBy,omitempty"`
	// Versions are the component's rates over time, non-overlapping.
	Versions []VersionDecl `json:"versions"`
}

// ApplyDecl is one applicability condition.
type ApplyDecl struct {
	// Roles is the effective customer roles matched; empty is any.
	Roles []string `json:"roles,omitempty"`
	// Supply is ANY, DOMESTIC or CROSS_BORDER: whether the seller is
	// established in the place of supply's country.
	Supply string `json:"supply"`
	// Treatment is CUSTOMER_CHARGE, PROVIDER_COST or REVERSE_CHARGE.
	Treatment string `json:"treatment"`
	// Responsibility names a ResponsibilityDecl.
	Responsibility string `json:"responsibility"`
}

// BaseDecl is the component's taxable base (DET-001 §3).
type BaseDecl struct {
	Kind       string   `json:"kind"`
	Components []string `json:"components,omitempty"`
}

// VersionDecl is one effective-dated rate.
type VersionDecl struct {
	// From and To are dates in the component jurisdiction's timezone; To is
	// exclusive and empty is open-ended.
	From string `json:"from"`
	To   string `json:"to,omitempty"`
	// Kind is AD_VALOREM, SPECIFIC or FLAT.
	Kind string `json:"kind"`
	// Rate is the proportion for AD_VALOREM ("0.20").
	Rate string `json:"rate,omitempty"`
	// Amount and Currency are the per-unit or flat amount.
	Amount   string `json:"amount,omitempty"`
	Currency string `json:"currency,omitempty"`
	// Unit is the quantity unit a SPECIFIC rate is per.
	Unit string `json:"unit,omitempty"`
	// LegalReason explains a zero rate: ZERO_RATED, EXEMPT or OUT_OF_SCOPE.
	// Required when the rate is zero, so a legally zero charge is never
	// indistinguishable from a missing one.
	LegalReason string `json:"legalReason,omitempty"`
	Source      string `json:"source"`
	Citation    string `json:"citation"`
}

// The treatments a component can give a line.
const (
	TreatmentCustomerCharge = "CUSTOMER_CHARGE"
	TreatmentProviderCost   = "PROVIDER_COST"
	TreatmentReverseCharge  = "REVERSE_CHARGE"
	// TreatmentExempt is never declared; it is what a valid certificate
	// makes of a CUSTOMER_CHARGE.
	TreatmentExempt = "EXEMPT"
)

// The supply conditions.
const (
	SupplyAny         = "ANY"
	SupplyDomestic    = "DOMESTIC"
	SupplyCrossBorder = "CROSS_BORDER"
)

// The responsible parties.
const (
	PartySeller = "SELLER"
	PartyBuyer  = "BUYER"
	PartyNone   = "NONE"
)

// The legal reasons a zero amount carries.
const (
	ReasonZeroRated  = "ZERO_RATED"
	ReasonExempt     = "EXEMPT"
	ReasonOutOfScope = "OUT_OF_SCOPE"
)

// ---------------------------------------------------------------------------
// compiled form
// ---------------------------------------------------------------------------

// Engine is a compiled Profile. It is immutable after Compile, so one Engine
// serves any number of concurrent determinations.
type Engine struct {
	version   string
	resolver  *jurisdiction.Resolver
	graph     *jurisdiction.Graph
	countries map[string]bool
	byCode    map[string]jurisdiction.ID
	// rules keeps each situs rule's declaration, to judge evidence the
	// graph cannot place against the rule that resolved without it.
	rules     map[string]SitusRuleDecl
	regimes   map[string]*regime // by country
	covered   []string
	resp      map[string]ResponsibilityDecl
	comps     []*component
	compIndex map[string]*component
	slots     []string
}

type regime struct {
	decl     RegimeDecl
	services map[string]bool
	level    jurisdiction.Level
}

type component struct {
	decl     ComponentDecl
	regime   *regime
	services map[string]bool
	versions []version
	exemptBy map[string]bool
	loc      *time.Location
}

type version struct {
	decl     VersionDecl
	from     time.Time
	to       *time.Time
	rate     determination.Rate
	reason   string
	isZero   bool
	perUnits string
}

// Version returns the profile's content version.
func (e *Engine) Version() string { return e.version }

// GraphVersion returns the jurisdiction graph version.
func (e *Engine) GraphVersion() string { return e.graph.Version() }

// Slots returns every result slot the profile may emit, sorted. A fiscal
// profile's postings and obligations are checked against these at load.
func (e *Engine) Slots() []string { return append([]string(nil), e.slots...) }

// Covered returns the ISO countries the profile has a regime for, sorted.
func (e *Engine) Covered() []string { return append([]string(nil), e.covered...) }

// The aggregate slots every taxation decision emits, so a posting profile can
// move totals by treatment without naming every component.
const (
	SlotCustomerTax     = "CUSTOMER_TAX"
	SlotProviderAccrual = "PROVIDER_ACCRUAL"
	SlotReverseCharge   = "REVERSE_CHARGE_SELF_ASSESSED"
)

// DecodeProfile reads a profile from its authored JSON, strictly: a field
// this build does not understand is a field whose meaning it would be
// guessing (ADR-0011 P5).
func DecodeProfile(data []byte) (Profile, error) {
	var p Profile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("taxation: decode profile: %w", err)
	}
	if dec.More() {
		return Profile{}, fmt.Errorf("taxation: decode profile: more than one document")
	}
	return p, nil
}

// Compile validates a profile and builds its engine. Every check that can be
// made before a request arrives is made here, so a bundle that loads is a
// bundle whose taxation content is coherent (ADR-0005 §2.6).
func Compile(p Profile) (*Engine, error) {
	if p.Version == "" || p.GraphVersion == "" {
		return nil, fmt.Errorf("taxation: profile names no version or no graph version")
	}
	graph, byCode, err := buildGraph(p)
	if err != nil {
		return nil, err
	}
	rules, err := buildSitus(p.Situs)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		version:   p.Version,
		graph:     graph,
		countries: map[string]bool{},
		rules:     map[string]SitusRuleDecl{},
		byCode:    byCode,
		resolver:  &jurisdiction.Resolver{Graph: graph, Rules: rules},
		regimes:   map[string]*regime{},
		resp:      map[string]ResponsibilityDecl{},
		compIndex: map[string]*component{},
	}
	for _, j := range p.Jurisdictions {
		if jurisdiction.Level(j.Level) == jurisdiction.LevelCountry {
			e.countries[j.Country] = true
		}
	}
	for _, r := range p.Situs.Rules {
		e.rules[r.ID] = r
	}
	for _, r := range p.Responsibility {
		if err := validResponsibility(r); err != nil {
			return nil, err
		}
		if _, dup := e.resp[r.ID]; dup {
			return nil, fmt.Errorf("taxation: responsibility %s is declared twice", r.ID)
		}
		e.resp[r.ID] = r
	}
	regimeByID := map[string]*regime{}
	for _, r := range p.Regimes {
		rg, err := e.buildRegime(r)
		if err != nil {
			return nil, err
		}
		if _, dup := e.regimes[r.Country]; dup {
			return nil, fmt.Errorf("taxation: country %s has two regimes", r.Country)
		}
		if _, dup := regimeByID[r.ID]; dup {
			return nil, fmt.Errorf("taxation: regime %s is declared twice", r.ID)
		}
		e.regimes[r.Country] = rg
		regimeByID[r.ID] = rg
		e.covered = append(e.covered, r.Country)
	}
	sort.Strings(e.covered)

	slots := map[string]bool{SlotCustomerTax: true, SlotProviderAccrual: true, SlotReverseCharge: true}
	for _, c := range p.Components {
		comp, err := e.buildComponent(c, regimeByID)
		if err != nil {
			return nil, err
		}
		if _, dup := e.compIndex[c.ID]; dup {
			return nil, fmt.Errorf("taxation: component %s is declared twice", c.ID)
		}
		e.compIndex[c.ID] = comp
		e.comps = append(e.comps, comp)
		slots[c.Emit] = true
	}
	sort.Slice(e.comps, func(i, j int) bool { return e.comps[i].decl.ID < e.comps[j].decl.ID })
	for s := range slots {
		e.slots = append(e.slots, s)
	}
	sort.Strings(e.slots)

	if err := e.checkBases(); err != nil {
		return nil, err
	}
	return e, nil
}

func buildGraph(p Profile) (*jurisdiction.Graph, map[string]jurisdiction.ID, error) {
	nodes := make([]jurisdiction.Node, 0, len(p.Jurisdictions))
	byCode := map[string]jurisdiction.ID{}
	for _, j := range p.Jurisdictions {
		loc, err := time.LoadLocation(j.Timezone)
		if err != nil || j.Timezone == "" {
			return nil, nil, fmt.Errorf("taxation: jurisdiction %s names timezone %q, which this runtime cannot load", j.ID, j.Timezone)
		}
		from, err := civilDate(j.EffectiveFrom, loc)
		if err != nil {
			return nil, nil, fmt.Errorf("taxation: jurisdiction %s effectiveFrom: %w", j.ID, err)
		}
		n := jurisdiction.Node{
			Jurisdiction: jurisdiction.Jurisdiction{
				ID: jurisdiction.ID(j.ID), Parent: jurisdiction.ID(j.Parent), Level: jurisdiction.Level(j.Level),
				Name: j.Name, Country: j.Country, Timezone: j.Timezone,
			},
			EffectiveFrom: from,
		}
		if j.EffectiveTo != "" {
			to, err := civilDate(j.EffectiveTo, loc)
			if err != nil {
				return nil, nil, fmt.Errorf("taxation: jurisdiction %s effectiveTo: %w", j.ID, err)
			}
			n.EffectiveTo = &to
		}
		if j.Code != "" {
			if !strings.HasPrefix(j.Code, j.Country+"-") {
				return nil, nil, fmt.Errorf("taxation: jurisdiction %s has code %q, which is not an ISO 3166-2 code in %s", j.ID, j.Code, j.Country)
			}
			if _, dup := byCode[j.Code]; dup {
				return nil, nil, fmt.Errorf("taxation: subdivision code %s names two jurisdictions", j.Code)
			}
			byCode[j.Code] = n.ID
		}
		nodes = append(nodes, n)
	}
	memberships := make([]jurisdiction.Membership, 0, len(p.Memberships))
	for _, m := range p.Memberships {
		ids := make([]jurisdiction.ID, len(m.Members))
		for i, id := range m.Members {
			ids[i] = jurisdiction.ID(id)
		}
		memberships = append(memberships, jurisdiction.Membership{Special: jurisdiction.ID(m.Special), Members: ids})
	}
	g, err := jurisdiction.NewGraph(p.GraphVersion, nodes, memberships)
	if err != nil {
		return nil, nil, err
	}
	return g, byCode, nil
}

func buildSitus(s SitusDecl) (*jurisdiction.RuleSet, error) {
	bindings := make([]jurisdiction.Binding, 0, len(s.Rules))
	for _, r := range s.Rules {
		if r.Service != "" && !ontology.Family(r.Service).Valid() {
			return nil, fmt.Errorf("taxation: situs rule %s selects service %q, which the ontology does not declare", r.ID, r.Service)
		}
		if jurisdiction.Mode(r.Mode) == jurisdiction.ModeSpatial {
			// A spatial rule pins a boundary dataset, and the taxation
			// section carries none yet. Refused rather than resolved
			// against nothing.
			return nil, fmt.Errorf("taxation: situs rule %s is SPATIAL; this profile format carries no boundary dataset", r.ID)
		}
		rel := make(map[jurisdiction.EvidenceType]jurisdiction.ReliabilityClass, len(r.Reliability))
		for t, c := range r.Reliability {
			rel[jurisdiction.EvidenceType(t)] = jurisdiction.ReliabilityClass(c)
		}
		bindings = append(bindings, jurisdiction.Binding{
			Ontology: r.Service,
			Role:     jurisdiction.CustomerRole(r.Role),
			Rule: jurisdiction.SitusRule{
				ID: r.ID, Mode: jurisdiction.Mode(r.Mode), Reliability: rel,
				Order: evidenceTypes(r.Order), MinimumReliability: jurisdiction.ReliabilityClass(r.MinimumReliability),
				Required: r.Required, Eligible: evidenceTypes(r.Eligible), AgreementLevel: jurisdiction.Level(r.AgreementLevel),
				Roaming: jurisdiction.RoamingSourcing(r.Roaming),
			},
		})
	}
	return jurisdiction.NewRuleSet(bindings, jurisdiction.CustomerRole(s.UndeterminedAs))
}

func evidenceTypes(ss []string) []jurisdiction.EvidenceType {
	out := make([]jurisdiction.EvidenceType, len(ss))
	for i, s := range ss {
		out[i] = jurisdiction.EvidenceType(s)
	}
	return out
}

func validResponsibility(r ResponsibilityDecl) error {
	if r.ID == "" {
		return fmt.Errorf("taxation: a responsibility assignment has no id")
	}
	for role, party := range map[string]string{
		"incidence": r.Incidence, "collection": r.Collection, "reporting": r.Reporting,
		"remittance": r.Remittance, "economicBearer": r.EconomicBearer,
	} {
		switch party {
		case PartySeller, PartyBuyer, PartyNone:
		default:
			// Every role is answered. An unanswered role is the single
			// "supplier type" field the playbook warns against, in disguise.
			return fmt.Errorf("taxation: responsibility %s assigns %s to %q; SELLER, BUYER or NONE", r.ID, role, party)
		}
	}
	return nil
}

func (e *Engine) buildRegime(r RegimeDecl) (*regime, error) {
	if r.ID == "" || r.Name == "" {
		return nil, fmt.Errorf("taxation: a regime has no id or no name")
	}
	if r.Source == "" || r.Citation == "" {
		return nil, fmt.Errorf("taxation: regime %s carries no source or citation", r.ID)
	}
	if !e.countries[r.Country] {
		return nil, fmt.Errorf("taxation: regime %s covers %s, which the graph does not declare as a country", r.ID, r.Country)
	}
	rg := &regime{decl: r, services: map[string]bool{}, level: jurisdiction.LevelCountry}
	if len(r.Services) == 0 {
		return nil, fmt.Errorf("taxation: regime %s covers no services", r.ID)
	}
	for _, s := range r.Services {
		if !ontology.Family(s).Valid() {
			return nil, fmt.Errorf("taxation: regime %s covers service %q, which the ontology does not declare", r.ID, s)
		}
		rg.services[s] = true
	}
	if r.RequiresLevel != "" {
		rg.level = jurisdiction.Level(r.RequiresLevel)
		if !rg.level.Valid() || rg.level == jurisdiction.LevelSpecial {
			return nil, fmt.Errorf("taxation: regime %s requires level %q", r.ID, r.RequiresLevel)
		}
	}
	if r.Extraction != nil && r.Extraction.Basis() != fiscal.BasisLine {
		return nil, fmt.Errorf("taxation: regime %s extracts at %s; extraction is per line", r.ID, r.Extraction.Basis())
	}
	for _, code := range r.ExcludedSubdivisions {
		if !strings.HasPrefix(code, r.Country+"-") {
			return nil, fmt.Errorf("taxation: regime %s excludes %q, which is not a subdivision of %s", r.ID, code, r.Country)
		}
		if _, declared := e.byCode[code]; declared {
			return nil, fmt.Errorf("taxation: regime %s excludes %s, which the graph declares as a jurisdiction", r.ID, code)
		}
	}
	return rg, nil
}

func (e *Engine) buildComponent(c ComponentDecl, regimes map[string]*regime) (*component, error) {
	if err := determination.ComponentID(c.ID).Validate(); err != nil {
		return nil, fmt.Errorf("taxation: %w", err)
	}
	switch {
	case c.Authority == "" || c.TaxType == "" || c.Name == "":
		return nil, fmt.Errorf("taxation: component %s names no authority, tax type or name", c.ID)
	case c.Emit == "" || !slotName(c.Emit):
		return nil, fmt.Errorf("taxation: component %s emits slot %q; a slot is SCREAMING_SNAKE_CASE", c.ID, c.Emit)
	case c.Emit == SlotCustomerTax || c.Emit == SlotProviderAccrual || c.Emit == SlotReverseCharge:
		return nil, fmt.Errorf("taxation: component %s emits %s, which is an aggregate slot", c.ID, c.Emit)
	}
	rg, ok := regimes[c.Regime]
	if !ok {
		return nil, fmt.Errorf("taxation: component %s belongs to regime %s, which is not declared", c.ID, c.Regime)
	}
	node, ok := e.graph.Node(jurisdiction.ID(c.Jurisdiction))
	if !ok {
		return nil, fmt.Errorf("taxation: component %s is levied in %s, which the graph does not declare", c.ID, c.Jurisdiction)
	}
	if node.Country != rg.decl.Country {
		return nil, fmt.Errorf("taxation: component %s is levied in %s (%s) under regime %s (%s)", c.ID, c.Jurisdiction, node.Country, rg.decl.ID, rg.decl.Country)
	}
	loc, err := time.LoadLocation(node.Timezone)
	if err != nil {
		return nil, fmt.Errorf("taxation: component %s: %w", c.ID, err)
	}
	if c.Rounding.Mode() == "" {
		return nil, fmt.Errorf("taxation: component %s carries no rounding policy", c.ID)
	}
	comp := &component{decl: c, regime: rg, services: map[string]bool{}, exemptBy: map[string]bool{}, loc: loc}
	for _, s := range c.Services {
		if !rg.services[s] {
			return nil, fmt.Errorf("taxation: component %s applies to %s, which regime %s does not cover", c.ID, s, rg.decl.ID)
		}
		comp.services[s] = true
	}
	for _, t := range c.ExemptBy {
		if t == "" {
			return nil, fmt.Errorf("taxation: component %s names an empty certificate type", c.ID)
		}
		comp.exemptBy[t] = true
	}
	if err := e.checkApplies(c); err != nil {
		return nil, err
	}
	switch c.Base.Kind {
	case string(determination.BaseLineNet), string(determination.BaseLineGross), string(determination.BaseQuantity):
		if len(c.Base.Components) > 0 {
			return nil, fmt.Errorf("taxation: component %s's %s base names components", c.ID, c.Base.Kind)
		}
	case string(determination.BaseNetPlusComponents), string(determination.BaseComponentSum):
		if len(c.Base.Components) == 0 {
			return nil, fmt.Errorf("taxation: component %s's %s base names no components", c.ID, c.Base.Kind)
		}
	default:
		return nil, fmt.Errorf("taxation: component %s has base kind %q", c.ID, c.Base.Kind)
	}
	if len(c.Versions) == 0 {
		return nil, fmt.Errorf("taxation: component %s has no rate versions", c.ID)
	}
	for i, v := range c.Versions {
		ver, err := buildVersion(c, v, loc)
		if err != nil {
			return nil, fmt.Errorf("taxation: component %s version %d: %w", c.ID, i, err)
		}
		comp.versions = append(comp.versions, ver)
	}
	sort.Slice(comp.versions, func(i, j int) bool { return comp.versions[i].from.Before(comp.versions[j].from) })
	for i := 1; i < len(comp.versions); i++ {
		prev := comp.versions[i-1]
		if prev.to == nil || prev.to.After(comp.versions[i].from) {
			return nil, fmt.Errorf("taxation: component %s has overlapping versions from %s and %s",
				c.ID, prev.decl.From, comp.versions[i].decl.From)
		}
	}
	return comp, nil
}

// checkApplies validates the applicability table and refuses two entries
// that could both match one line: the tie would be broken by declaration
// order, which is an ordering nobody reviewed as law (JUR-001 §4.5.2's
// reasoning, applied to treatments).
func (e *Engine) checkApplies(c ComponentDecl) error {
	if len(c.Applies) == 0 {
		return fmt.Errorf("taxation: component %s declares no applicability", c.ID)
	}
	for i, a := range c.Applies {
		switch a.Supply {
		case SupplyAny, SupplyDomestic, SupplyCrossBorder:
		default:
			return fmt.Errorf("taxation: component %s applies entry %d has supply %q", c.ID, i, a.Supply)
		}
		switch a.Treatment {
		case TreatmentCustomerCharge, TreatmentProviderCost, TreatmentReverseCharge:
		default:
			return fmt.Errorf("taxation: component %s applies entry %d has treatment %q", c.ID, i, a.Treatment)
		}
		if _, ok := e.resp[a.Responsibility]; !ok {
			return fmt.Errorf("taxation: component %s applies entry %d names responsibility %q, which is not declared", c.ID, i, a.Responsibility)
		}
		for _, r := range a.Roles {
			if !jurisdiction.CustomerRole(r).Valid() {
				return fmt.Errorf("taxation: component %s applies entry %d names role %q", c.ID, i, r)
			}
		}
		for j := 0; j < i; j++ {
			if overlaps(c.Applies[j], a) {
				return fmt.Errorf("taxation: component %s applies entries %d and %d can both match one line", c.ID, j, i)
			}
		}
	}
	return nil
}

func overlaps(a, b ApplyDecl) bool {
	supply := a.Supply == SupplyAny || b.Supply == SupplyAny || a.Supply == b.Supply
	if !supply {
		return false
	}
	if len(a.Roles) == 0 || len(b.Roles) == 0 {
		return true
	}
	for _, x := range a.Roles {
		for _, y := range b.Roles {
			if x == y {
				return true
			}
		}
	}
	return false
}

func buildVersion(c ComponentDecl, v VersionDecl, loc *time.Location) (version, error) {
	from, err := civilDate(v.From, loc)
	if err != nil {
		return version{}, fmt.Errorf("from: %w", err)
	}
	ver := version{decl: v, from: from, reason: v.LegalReason}
	if v.To != "" {
		to, err := civilDate(v.To, loc)
		if err != nil {
			return version{}, fmt.Errorf("to: %w", err)
		}
		if !to.After(from) {
			return version{}, fmt.Errorf("ends on or before it starts")
		}
		ver.to = &to
	}
	if v.Source == "" || v.Citation == "" {
		return version{}, fmt.Errorf("carries no source or citation")
	}
	switch v.Kind {
	case string(determination.RateAdValorem):
		if c.Base.Kind == string(determination.BaseQuantity) {
			return version{}, fmt.Errorf("applies a proportion to a quantity")
		}
		r, err := fiscal.ParseRate(v.Rate, fiscal.RateBasisNet)
		if err != nil {
			return version{}, err
		}
		if v.Amount != "" || v.Currency != "" || v.Unit != "" {
			return version{}, fmt.Errorf("an AD_VALOREM rate carries an amount, currency or unit")
		}
		ver.rate = determination.Rate{Kind: determination.RateAdValorem, Proportion: &r}
		d := r.Decimal()
		ver.isZero = d.IsZero()
	case string(determination.RateSpecific), string(determination.RateFlat):
		m, err := fiscal.ParseMoney(v.Amount, fiscal.Currency(v.Currency))
		if err != nil {
			return version{}, err
		}
		if v.Rate != "" {
			return version{}, fmt.Errorf("a %s rate carries a proportion", v.Kind)
		}
		ver.isZero = m.IsZero()
		if v.Kind == string(determination.RateSpecific) {
			if c.Base.Kind != string(determination.BaseQuantity) {
				return version{}, fmt.Errorf("a SPECIFIC rate needs a QUANTITY base")
			}
			if v.Unit == "" {
				return version{}, fmt.Errorf("a SPECIFIC rate names no unit")
			}
			ver.perUnits = v.Unit
			ver.rate = determination.Rate{Kind: determination.RateSpecific, PerUnit: &m}
		} else {
			if v.Unit != "" {
				return version{}, fmt.Errorf("a FLAT rate names a unit")
			}
			ver.rate = determination.Rate{Kind: determination.RateFlat, Flat: &m}
		}
	default:
		return version{}, fmt.Errorf("rate kind %q; AD_VALOREM, SPECIFIC or FLAT", v.Kind)
	}
	switch v.LegalReason {
	case "":
		if ver.isZero {
			// A zero rate with no legal reason is indistinguishable from a
			// rate somebody forgot to fill in (playbook §12).
			return version{}, fmt.Errorf("a zero rate carries no legal reason")
		}
	case ReasonZeroRated, ReasonExempt, ReasonOutOfScope:
		if !ver.isZero {
			return version{}, fmt.Errorf("a non-zero rate carries legal reason %s", v.LegalReason)
		}
	default:
		return version{}, fmt.Errorf("legal reason %q", v.LegalReason)
	}
	return ver, nil
}

// checkBases refuses a compounding base that names an undeclared component
// or one in another regime, and proves the whole component graph acyclic by
// compiling it once — so a request-time compile of any subset cannot fail on
// content.
func (e *Engine) checkBases() error {
	var all []determination.Component
	for _, c := range e.comps {
		for _, ref := range c.decl.Base.Components {
			dep, ok := e.compIndex[ref]
			if !ok {
				return fmt.Errorf("taxation: component %s's base names %s, which is not declared", c.decl.ID, ref)
			}
			if dep.regime != c.regime {
				return fmt.Errorf("taxation: component %s compounds on %s from another regime", c.decl.ID, ref)
			}
		}
		latest := c.versions[len(c.versions)-1]
		all = append(all, c.planComponent(latest, c.decl.ID, false))
	}
	if len(all) == 0 {
		return fmt.Errorf("taxation: profile declares no components")
	}
	if _, err := determination.Compile(determination.Schedule{Components: all}); err != nil {
		return fmt.Errorf("taxation: %w", err)
	}
	return nil
}

// planComponent renders the component at one version into the DET-001
// component the evaluator runs, under a plan-local identifier.
func (c *component) planComponent(v version, planID string, inclusive bool) determination.Component {
	base := determination.Base{Kind: determination.BaseKind(c.decl.Base.Kind)}
	for _, ref := range c.decl.Base.Components {
		base.Components = append(base.Components, determination.ComponentID(ref))
	}
	return determination.Component{
		ID:             determination.ComponentID(planID),
		JurisdictionID: c.decl.Jurisdiction,
		TaxType:        c.decl.TaxType,
		Base:           base,
		Rate:           v.rate,
		Policy:         c.decl.Rounding,
		Inclusive:      inclusive,
	}
}

// versionAt returns the version effective at t.
func (c *component) versionAt(t time.Time) (version, bool) {
	for _, v := range c.versions {
		if !t.Before(v.from) && (v.to == nil || t.Before(*v.to)) {
			return v, true
		}
	}
	return version{}, false
}

// civilDate reads a YYYY-MM-DD date as midnight in loc: "from 1 April" in an
// instrument means midnight in the authority's own calendar, not UTC
// (ADR-0003 §2.6, playbook §9 "Period boundary").
func civilDate(s string, loc *time.Location) (time.Time, error) {
	d, err := time.ParseInLocation(time.DateOnly, s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a YYYY-MM-DD date", s)
	}
	return d, nil
}

func slotName(s string) bool {
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9' && i > 0, r == '_' && i > 0:
		default:
			return false
		}
	}
	return s != ""
}
