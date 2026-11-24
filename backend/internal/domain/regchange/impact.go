package regchange

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// The impact assessment (Doc 03 §04 impact_assessment, WF-09).
//
// Assess is a pure function. Its inputs are the change's legal particulars,
// the active content bundle's coverage at the change's effective date, and
// the tenant's own registrations and open obligations; its output is every
// impacted item with a severity and the exact basis on which it was found,
// and an overall severity. The inputs digest covers everything Assess read
// and the assessment digest covers what it found, so the same inputs give
// the same digest and a different digest says something changed (WF-09
// "deterministic replay"). Nothing here computes an amount, and nothing
// writes a tax decision, a ledger posting or a filing (Z03-AT-17).

// Severity is how much an item, or a whole assessment, matters.
type Severity string

// The severities, ascending.
const (
	SeverityLow      Severity = "LOW"
	SeverityMedium   Severity = "MEDIUM"
	SeverityHigh     Severity = "HIGH"
	SeverityCritical Severity = "CRITICAL"
)

// Severities is every severity, ascending.
var Severities = []Severity{SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical}

// Valid reports whether the severity is one of these.
func (s Severity) Valid() bool { return slices.Contains(Severities, s) }

func (s Severity) rank() int { return slices.Index(Severities, s) }

// lower is one step down, never below LOW.
func (s Severity) lower() Severity {
	if s.rank() <= SeverityLow.rank() {
		return SeverityLow
	}
	return Severities[s.rank()-1]
}

// kindSeverity is the severity a change of a kind carries for what it
// touches (Doc 03 §06's required system action, read as urgency).
var kindSeverity = map[Kind]Severity{
	KindBackdatedRule:      SeverityCritical,
	KindNewLevy:            SeverityHigh,
	KindRateChange:         SeverityHigh,
	KindBaseChange:         SeverityHigh,
	KindClassChange:        SeverityHigh,
	KindRightsWithdrawal:   SeverityHigh,
	KindRepealSunset:       SeverityMedium,
	KindJurisdictionChange: SeverityMedium,
	KindSourceDispute:      SeverityMedium,
}

// ItemKind is what an impacted item is.
type ItemKind string

// The item kinds.
const (
	// ItemContentComponent is a component of the active content bundle in
	// force at the effective date in the change's scope: the rule pack
	// content that may need to change.
	ItemContentComponent ItemKind = "CONTENT_COMPONENT"
	// ItemCoverageGap is the change's jurisdiction with no matching
	// component: the active content does not determine it, and a
	// determination there answers UNSUPPORTED until a pack covers it.
	ItemCoverageGap ItemKind = "COVERAGE_GAP"
	// ItemTenantRegistration is one of the tenant's registrations in the
	// change's jurisdiction.
	ItemTenantRegistration ItemKind = "TENANT_REGISTRATION"
	// ItemTenantObligation is one of the tenant's open obligations in the
	// jurisdiction whose period ends on or after the effective date.
	ItemTenantObligation ItemKind = "TENANT_OBLIGATION"
)

var itemOrder = []ItemKind{ItemContentComponent, ItemCoverageGap, ItemTenantRegistration, ItemTenantObligation}

// Valid reports whether the item kind is one of these.
func (k ItemKind) Valid() bool { return slices.Contains(itemOrder, k) }

// Content reports whether the item is about the content rather than the
// tenant's own records.
func (k ItemKind) Content() bool { return k == ItemContentComponent || k == ItemCoverageGap }

// ContentComponent is one component of the active content, as coverage
// reports it in force at an instant.
type ContentComponent struct {
	ID           string // the component's id in the pack
	Regime       string // the regime (country pack) it belongs to
	Jurisdiction string // the content jurisdiction: jurisdiction:us-ny
	Authority    string
	TaxType      string
	Name         string
	Services     []string // empty for every service of the regime
	From, To     string   // the version in force: its legal interval
	Citation     string
}

// ContentSnapshot is the active content bundle's coverage at the change's
// effective date.
type ContentSnapshot struct {
	BundleID     string
	BundleDigest string
	At           time.Time
	Components   []ContentComponent
}

// RegistrationRef is one of the tenant's registrations, as an assessment
// reads it.
type RegistrationRef struct {
	ID           string
	Scheme       string
	Jurisdiction string // US, US-NY
	Authority    string
	Status       string
	Version      int
}

// ObligationRef is one of the tenant's obligations, as an assessment reads
// it.
type ObligationRef struct {
	ID           string
	BusinessKey  string
	Jurisdiction string // the content jurisdiction: jurisdiction:us-ny
	Type         string
	Status       string
	PeriodStart  time.Time
	PeriodEnd    time.Time
}

// Inputs is everything an assessment reads.
type Inputs struct {
	Change        Change
	Content       ContentSnapshot
	Registrations []RegistrationRef
	Obligations   []ObligationRef
}

// Item is one impacted item, with its severity and the basis on which it
// was found.
type Item struct {
	Kind         ItemKind
	Ref          string
	Label        string
	Jurisdiction string
	Severity     Severity
	Basis        string
	// Retrospective is an obligation period the change reaches back into:
	// effective before it was published (Doc 03 §06 BACKDATED_RULE).
	Retrospective bool
}

// Result is what Assess found.
type Result struct {
	Items            []Item
	Severity         Severity
	Retroactive      bool
	InputsDigest     canonical.Digest
	AssessmentDigest canonical.Digest
}

// Assessment is a recorded impact assessment. It is immutable.
type Assessment struct {
	ID       id.ImpactAssessmentID
	TenantID id.TenantID
	Change   id.RegulatoryChangeID

	BundleID     string
	BundleDigest string
	AsOf         time.Time // the effective date the content was read at

	Result
	// Counts of what was read, for the screen.
	ComponentsRead    int
	RegistrationsRead int
	ObligationsRead   int

	CreatedAt time.Time
	CreatedBy id.UserID
}

// ContentJurisdiction is how the content packs name a jurisdiction:
// US-NY is jurisdiction:us-ny.
func ContentJurisdiction(j string) string { return "jurisdiction:" + strings.ToLower(j) }

// inScope reports whether a jurisdiction (in the content's form) is the
// change's jurisdiction or one inside it: a country's change reaches its
// subdivisions, a state's change its localities.
func inScope(changeJurisdiction, contentJurisdiction string) bool {
	cj := ContentJurisdiction(changeJurisdiction)
	return contentJurisdiction == cj || strings.HasPrefix(contentJurisdiction, cj+"-")
}

// openRegistration is a registration status that is not closed.
func openRegistration(s string) bool {
	switch s {
	case "SUBMITTED", "VERIFIED", "ACTIVE", "SUSPENDED":
		return true
	}
	return false
}

// settledObligation is an obligation status past filing: a change reaching
// it is a correction, which is a separately authorized workflow.
func settledObligation(s string) bool {
	switch s {
	case "FILED", "ACCEPTED", "PAID", "PAYMENT_DUE":
		return true
	}
	return false
}

func overlaps(a, b []string) bool {
	for _, s := range a {
		if slices.Contains(b, s) {
			return true
		}
	}
	return false
}

// Assess assesses a change's impact. It is deterministic: the order of the
// inputs does not matter, and the same inputs give the same digests.
func Assess(in Inputs) (Result, error) {
	c := in.Change
	base := kindSeverity[c.Kind]
	date := c.EffectiveFrom.Format(time.DateOnly)
	var items []Item

	matchedTax := false
	for _, comp := range in.Content.Components {
		if !inScope(c.Jurisdiction, comp.Jurisdiction) {
			continue
		}
		if len(c.TaxTypes) > 0 && !slices.Contains(c.TaxTypes, comp.TaxType) {
			continue
		}
		if len(c.ServiceFamilies) > 0 && len(comp.Services) > 0 && !overlaps(c.ServiceFamilies, comp.Services) {
			continue
		}
		matchedTax = true
		basis := fmt.Sprintf("Component %s (%s, %s) of bundle %s is in force on %s in %s, within the change's jurisdiction %s",
			comp.ID, comp.TaxType, comp.Authority, in.Content.BundleID, date, comp.Jurisdiction, c.Jurisdiction)
		if len(c.TaxTypes) > 0 {
			basis += "; its tax type is in the change's scope"
		}
		if len(c.ServiceFamilies) > 0 {
			if len(comp.Services) == 0 {
				basis += "; it applies to every service of its regime"
			} else {
				basis += "; it applies to a service family in the change's scope"
			}
		}
		basis += fmt.Sprintf(". Version interval %s to %s, cited as %q.", comp.From, orOpen(comp.To), comp.Citation)
		items = append(items, Item{Kind: ItemContentComponent, Ref: comp.ID, Label: comp.Name, Jurisdiction: comp.Jurisdiction,
			Severity: base, Basis: basis})
	}
	if !matchedTax {
		sev := SeverityLow
		if c.Kind == KindNewLevy {
			sev = SeverityHigh
		}
		basis := fmt.Sprintf("No component of bundle %s in force on %s matches %s", orNone(in.Content.BundleID), date, c.Jurisdiction)
		if len(c.TaxTypes) > 0 {
			basis += " and tax types " + strings.Join(c.TaxTypes, ", ")
		}
		basis += ". The active content does not determine this; a determination in scope answers UNSUPPORTED until an approved pack covers it."
		items = append(items, Item{Kind: ItemCoverageGap, Ref: ContentJurisdiction(c.Jurisdiction), Label: "No matching content",
			Jurisdiction: ContentJurisdiction(c.Jurisdiction), Severity: sev, Basis: basis})
	}

	for _, r := range in.Registrations {
		if !openRegistration(r.Status) || !inScope(c.Jurisdiction, ContentJurisdiction(r.Jurisdiction)) {
			continue
		}
		sev := base
		basis := fmt.Sprintf("The tenant's %s registration with %s in %s is %s (version %d)", r.Scheme, r.Authority, r.Jurisdiction, r.Status, r.Version)
		if r.Status != "ACTIVE" {
			sev = sev.lower()
			basis += "; it is not active, so the exposure is prospective"
		}
		items = append(items, Item{Kind: ItemTenantRegistration, Ref: r.ID, Label: r.Scheme + " " + r.Jurisdiction,
			Jurisdiction: r.Jurisdiction, Severity: sev, Basis: basis + "."})
	}

	retro := c.Retroactive()
	for _, o := range in.Obligations {
		if o.Status == "CLOSED" || !inScope(c.Jurisdiction, o.Jurisdiction) || o.PeriodEnd.Before(c.EffectiveFrom) {
			continue
		}
		sev := base
		basis := fmt.Sprintf("The tenant's %s obligation %s for %s to %s is %s, and its period ends on or after the effective date %s",
			o.Type, o.BusinessKey, o.PeriodStart.Format(time.DateOnly), o.PeriodEnd.Format(time.DateOnly), o.Status, date)
		retrospective := retro && o.PeriodStart.Before(c.PublishedOn)
		if retrospective {
			sev = SeverityCritical
			basis += fmt.Sprintf("; the change was published on %s, after the period began, so it reaches back into it", c.PublishedOn.Format(time.DateOnly))
		}
		if settledObligation(o.Status) {
			basis += "; it is already filed or paid, and any correction is a separately authorized workflow"
		}
		items = append(items, Item{Kind: ItemTenantObligation, Ref: o.ID, Label: o.Type + " " + o.BusinessKey,
			Jurisdiction: o.Jurisdiction, Severity: sev, Basis: basis + ".", Retrospective: retrospective})
	}

	slices.SortFunc(items, func(a, b Item) int {
		if d := cmp.Compare(slices.Index(itemOrder, a.Kind), slices.Index(itemOrder, b.Kind)); d != 0 {
			return d
		}
		return cmp.Compare(a.Ref, b.Ref)
	})
	// There is always at least one item: the components found, or the gap.
	overall := SeverityLow
	for _, it := range items {
		if it.Severity.rank() > overall.rank() {
			overall = it.Severity
		}
	}
	res := Result{Items: items, Severity: overall, Retroactive: retro}
	var err error
	if res.InputsDigest, err = canonical.Sum(inputsValue(in)); err != nil {
		return Result{}, err
	}
	res.AssessmentDigest, err = canonical.Sum(res.Value())
	return res, err
}

func orOpen(s string) string {
	if s == "" {
		return "open"
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "(no active bundle)"
	}
	return s
}

// inputsValue is the canonical document of everything Assess read, sorted
// so that the order the inputs arrived in does not matter.
func inputsValue(in Inputs) canonical.Value {
	c := in.Change
	comps := slices.Clone(in.Content.Components)
	slices.SortFunc(comps, func(a, b ContentComponent) int { return cmp.Compare(a.ID+"\x00"+a.From, b.ID+"\x00"+b.From) })
	cv := make([]canonical.Value, len(comps))
	for i, x := range comps {
		services := slices.Clone(x.Services)
		slices.Sort(services)
		cv[i] = canonical.Object(
			canonical.F("id", canonical.String(x.ID)), canonical.F("regime", canonical.String(x.Regime)),
			canonical.F("jurisdiction", canonical.String(x.Jurisdiction)), canonical.F("authority", canonical.String(x.Authority)),
			canonical.F("taxType", canonical.String(x.TaxType)), canonical.F("name", canonical.String(x.Name)),
			canonical.F("services", stringsValue(services)), canonical.F("from", canonical.String(x.From)),
			canonical.F("to", canonical.OptString(x.To)), canonical.F("citation", canonical.String(x.Citation)),
		)
	}
	regs := slices.Clone(in.Registrations)
	slices.SortFunc(regs, func(a, b RegistrationRef) int { return cmp.Compare(a.ID, b.ID) })
	rv := make([]canonical.Value, len(regs))
	for i, r := range regs {
		rv[i] = canonical.Object(
			canonical.F("id", canonical.String(r.ID)), canonical.F("scheme", canonical.String(r.Scheme)),
			canonical.F("jurisdiction", canonical.String(r.Jurisdiction)), canonical.F("authority", canonical.String(r.Authority)),
			canonical.F("status", canonical.String(r.Status)), canonical.F("version", canonical.Integer(int64(r.Version))),
		)
	}
	obls := slices.Clone(in.Obligations)
	slices.SortFunc(obls, func(a, b ObligationRef) int { return cmp.Compare(a.ID, b.ID) })
	ov := make([]canonical.Value, len(obls))
	for i, o := range obls {
		ov[i] = canonical.Object(
			canonical.F("id", canonical.String(o.ID)), canonical.F("businessKey", canonical.String(o.BusinessKey)),
			canonical.F("jurisdiction", canonical.String(o.Jurisdiction)), canonical.F("type", canonical.String(o.Type)),
			canonical.F("status", canonical.String(o.Status)),
			canonical.F("periodStart", canonical.String(o.PeriodStart.Format(time.DateOnly))),
			canonical.F("periodEnd", canonical.String(o.PeriodEnd.Format(time.DateOnly))),
		)
	}
	return canonical.Object(
		canonical.F("change", canonical.Object(
			canonical.F("id", canonical.String(c.ID.String())),
			canonical.F("jurisdiction", canonical.String(c.Jurisdiction)),
			canonical.F("kind", canonical.String(string(c.Kind))),
			canonical.F("publishedOn", canonical.String(c.PublishedOn.Format(time.DateOnly))),
			canonical.F("effectiveFrom", canonical.String(c.EffectiveFrom.Format(time.DateOnly))),
			canonical.F("serviceFamilies", stringsValue(c.ServiceFamilies)),
			canonical.F("taxTypes", stringsValue(c.TaxTypes)),
			canonical.F("dedupeKey", canonical.String(c.DedupeKey.String())),
		)),
		canonical.F("content", canonical.Object(
			canonical.F("bundleId", canonical.String(in.Content.BundleID)),
			canonical.F("bundleDigest", canonical.String(in.Content.BundleDigest)),
			canonical.F("at", canonical.String(in.Content.At.Format(time.DateOnly))),
			canonical.F("components", canonical.Array(cv...)),
		)),
		canonical.F("registrations", canonical.Array(rv...)),
		canonical.F("obligations", canonical.Array(ov...)),
	)
}

// Value is the canonical document of an assessment's findings. Its digest is
// the assessment digest.
func (r Result) Value() canonical.Value {
	iv := make([]canonical.Value, len(r.Items))
	for i, it := range r.Items {
		iv[i] = canonical.Object(
			canonical.F("kind", canonical.String(string(it.Kind))), canonical.F("ref", canonical.String(it.Ref)),
			canonical.F("label", canonical.String(it.Label)), canonical.F("jurisdiction", canonical.String(it.Jurisdiction)),
			canonical.F("severity", canonical.String(string(it.Severity))), canonical.F("basis", canonical.String(it.Basis)),
			canonical.F("retrospective", canonical.Bool(it.Retrospective)),
		)
	}
	return canonical.Object(
		canonical.F("inputsDigest", canonical.String(r.InputsDigest.String())),
		canonical.F("items", canonical.Array(iv...)),
		canonical.F("severity", canonical.String(string(r.Severity))),
		canonical.F("retroactive", canonical.Bool(r.Retroactive)),
	)
}

// Verify recomputes a stored assessment's digest from its findings, so a
// stored row whose items were altered is detected rather than shown.
func (a Assessment) Verify() error {
	got, err := canonical.Sum(a.Value())
	if err != nil {
		return err
	}
	if !got.Equal(a.AssessmentDigest) {
		return fmt.Errorf("regchange: assessment %s does not match its digest", a.ID)
	}
	return nil
}

// HasItem reports whether the assessment found an item of this kind and
// ref.
func (a Assessment) HasItem(kind ItemKind, ref string) bool {
	return slices.ContainsFunc(a.Items, func(it Item) bool { return it.Kind == kind && it.Ref == ref })
}
