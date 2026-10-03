// Package ontology is ZoikoTax's canonical product and service ontology
// (ZTAX-CLS-001): what a telecom product is, independent of any customer's
// catalog, any vendor's code and any jurisdiction's tax conclusion about it
// (ZTAX-CLS-REQ-0001, -0003).
//
// The ontology says what a thing is. It does not say how it is taxed — that
// is a classification decision, made per jurisdiction and per regime, in
// package classification — and it does not say what a customer calls it —
// that is a catalog mapping, which points at the ontology and never becomes
// part of it.
package ontology

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/classification"
)

// ID is an ontology node's canonical identity: a stable content identifier
// under the ontology: grammar, never a SKU name, a vendor id or a TM Forum id
// (ZTAX-CLS-REQ-0009 to -0012), and never reused for something else
// (ZTAX-CLS-REQ-0002).
type ID = classification.OntologyID

// Family is a service family. Technology generations, access media and
// identity form factors are qualifiers on a family, not families
// (ZTAX-CLS-REQ-0016 to -0021): 5G voice is mobile voice.
type Family string

// The families the launch-critical corpus covers (ZTAX-CLS-REQ-0022 to -0031).
const (
	FamilyMobileVoice          Family = "MOBILE_VOICE"
	FamilyFixedVoice           Family = "FIXED_VOICE"
	FamilyMessaging            Family = "MESSAGING"
	FamilyMobileData           Family = "MOBILE_DATA"
	FamilyFixedBroadband       Family = "FIXED_BROADBAND"
	FamilyVoIP                 Family = "VOIP"
	FamilySIPTrunking          Family = "SIP_TRUNKING"
	FamilyUCaaS                Family = "UCAAS"
	FamilyCCaaS                Family = "CCAAS"
	FamilyCPaaS                Family = "CPAAS"
	FamilyIoTConnectivity      Family = "IOT_CONNECTIVITY"
	FamilySubscriptionIdentity Family = "SUBSCRIPTION_IDENTITY"
	FamilySatellite            Family = "SATELLITE_CONNECTIVITY"
	FamilyRoamingRetail        Family = "ROAMING_RETAIL"
	FamilyRoamingWholesale     Family = "ROAMING_WHOLESALE"
	FamilyInterconnect         Family = "INTERCONNECT"
	FamilyWholesaleAccess      Family = "WHOLESALE_ACCESS"
	FamilyEquipmentSale        Family = "EQUIPMENT_SALE"
	FamilyEquipmentRental      Family = "EQUIPMENT_RENTAL"
	FamilyInstallation         Family = "INSTALLATION_ACTIVATION"
	FamilyProfessionalServices Family = "PROFESSIONAL_SERVICES"
	FamilyContentMedia         Family = "CONTENT_MEDIA"
	FamilySoftware             Family = "SOFTWARE"
	// FamilyOtherNonTelecom is for products that are genuinely not telecom.
	// It is never where an unresolved telecom product goes
	// (ZTAX-CLS-REQ-0032, -0080): that is UNSUPPORTED.
	FamilyOtherNonTelecom Family = "OTHER_NON_TELECOM"
)

var families = map[Family]bool{
	FamilyMobileVoice: true, FamilyFixedVoice: true, FamilyMessaging: true, FamilyMobileData: true,
	FamilyFixedBroadband: true, FamilyVoIP: true, FamilySIPTrunking: true, FamilyUCaaS: true, FamilyCCaaS: true,
	FamilyCPaaS: true, FamilyIoTConnectivity: true, FamilySubscriptionIdentity: true, FamilySatellite: true,
	FamilyRoamingRetail: true, FamilyRoamingWholesale: true, FamilyInterconnect: true, FamilyWholesaleAccess: true,
	FamilyEquipmentSale: true, FamilyEquipmentRental: true, FamilyInstallation: true, FamilyProfessionalServices: true,
	FamilyContentMedia: true, FamilySoftware: true, FamilyOtherNonTelecom: true,
}

// Valid reports whether f is a known family.
func (f Family) Valid() bool { return families[f] }

// Qualifier is a technical attribute that refines a node without creating a
// new family.
type Qualifier string

// The qualifiers.
const (
	Qualifier4G    Qualifier = "4G"
	Qualifier5G    Qualifier = "5G"
	QualifierFiber Qualifier = "FIBER"
	QualifierCable Qualifier = "CABLE"
	QualifierDSL   Qualifier = "DSL"
	QualifierFWA   Qualifier = "FWA"
	QualifierVoLTE Qualifier = "VOLTE"
	QualifierVoNR  Qualifier = "VONR"
	QualifierLTEM  Qualifier = "LTE_M"
	QualifierNBIoT Qualifier = "NB_IOT"
	QualifierESIM  Qualifier = "ESIM"
	QualifierPSIM  Qualifier = "PSIM"
)

var qualifiers = map[Qualifier]bool{
	Qualifier4G: true, Qualifier5G: true, QualifierFiber: true, QualifierCable: true, QualifierDSL: true,
	QualifierFWA: true, QualifierVoLTE: true, QualifierVoNR: true, QualifierLTEM: true, QualifierNBIoT: true,
	QualifierESIM: true, QualifierPSIM: true,
}

// Valid reports whether q is a known qualifier.
func (q Qualifier) Valid() bool { return qualifiers[q] }

// EconomicNature is what a component is economically (ZTAX-CLS-REQ-0033).
type EconomicNature string

// The natures.
const (
	NatureService     EconomicNature = "SERVICE"
	NatureGoodSale    EconomicNature = "GOOD_SALE"
	NatureGoodRental  EconomicNature = "GOOD_RENTAL"
	NatureRight       EconomicNature = "RIGHT"
	NatureDigitalGood EconomicNature = "DIGITAL_GOOD"
)

// NetworkRole is retail or wholesale (ZTAX-CLS-REQ-0035).
type NetworkRole string

// The roles.
const (
	RoleRetail    NetworkRole = "RETAIL"
	RoleWholesale NetworkRole = "WHOLESALE"
)

// Lifecycle is a node's lifecycle state (ZTAX-CLS-REQ-0014).
type Lifecycle string

// The lifecycle states.
const (
	LifecycleDraft      Lifecycle = "DRAFT"
	LifecycleActive     Lifecycle = "ACTIVE"
	LifecycleDeprecated Lifecycle = "DEPRECATED"
	LifecycleRetired    Lifecycle = "RETIRED"
)

// Node is one version of an ontology node.
type Node struct {
	ID      ID
	Version int
	Family  Family
	Name    string
	// Definition is the precise, non-tax definition (ZTAX-CLS-REQ-0013).
	Definition string
	Owner      string
	Lifecycle  Lifecycle
	Nature     EconomicNature
	// Mode is the communication mode, where one applies (ZTAX-CLS-REQ-0034).
	Mode string
	Role NetworkRole
	// MeterBasis lists the meters usage is measured in (ZTAX-CLS-REQ-0036).
	MeterBasis []string
	// ValidFrom and ValidTo are valid time; RecordedAt is when the ontology
	// came to know this version (ZTAX-CLS-REQ-0098, -0099).
	ValidFrom  time.Time
	ValidTo    *time.Time
	RecordedAt time.Time
	// ReplacedBy names the successor of a deprecated or retired node
	// (ZTAX-CLS-REQ-0095).
	ReplacedBy []ID
}

// ValidAt reports whether this version is valid at t.
func (n Node) ValidAt(t time.Time) bool {
	return !t.Before(n.ValidFrom) && (n.ValidTo == nil || t.Before(*n.ValidTo))
}

// taxWords are phrases a definition may not carry: a node definition that
// says "exempt" or "taxable" has embedded a jurisdiction's conclusion
// (ZTAX-CLS-REQ-0003).
var taxWords = []string{"taxable", "exempt", "zero-rated", "zero rated", "vat ", "sales tax", "gst"}

func (n Node) validate() error {
	if err := n.ID.Validate(); err != nil {
		return err
	}
	switch {
	case n.Version < 1:
		return fmt.Errorf("ontology: %s has version %d", n.ID, n.Version)
	case !n.Family.Valid():
		return fmt.Errorf("ontology: %s has family %q", n.ID, n.Family)
	case n.Name == "" || n.Definition == "":
		return fmt.Errorf("ontology: %s v%d has no name or no definition", n.ID, n.Version)
	case n.Owner == "":
		return fmt.Errorf("ontology: %s v%d has no owner", n.ID, n.Version)
	case n.ValidFrom.IsZero() || n.RecordedAt.IsZero():
		return fmt.Errorf("ontology: %s v%d has no valid time or no recorded time", n.ID, n.Version)
	case n.ValidTo != nil && !n.ValidTo.After(n.ValidFrom):
		return fmt.Errorf("ontology: %s v%d ends before it starts", n.ID, n.Version)
	}
	switch n.Lifecycle {
	case LifecycleDraft, LifecycleActive, LifecycleRetired:
	case LifecycleDeprecated:
		if len(n.ReplacedBy) == 0 {
			return fmt.Errorf("ontology: deprecated %s names no replacement", n.ID)
		}
	default:
		return fmt.Errorf("ontology: %s has lifecycle %q", n.ID, n.Lifecycle)
	}
	switch n.Nature {
	case NatureService, NatureGoodSale, NatureGoodRental, NatureRight, NatureDigitalGood:
	default:
		return fmt.Errorf("ontology: %s names no economic nature", n.ID)
	}
	lower := strings.ToLower(n.Definition + " ")
	for _, w := range taxWords {
		if strings.Contains(lower, w) {
			return fmt.Errorf("ontology: %s's definition says %q; a node defines what a thing is, not how it is taxed", n.ID, strings.TrimSpace(w))
		}
	}
	return nil
}

// RelationshipKind is OntologyRelationship's kind.
type RelationshipKind string

// The relationship kinds.
const (
	RelPartOf     RelationshipKind = "PART_OF"
	RelSplitInto  RelationshipKind = "SPLIT_INTO"
	RelMergedInto RelationshipKind = "MERGED_INTO"
)

// Relationship is one OntologyRelationship.
type Relationship struct {
	From ID
	To   ID
	Kind RelationshipKind
}

// Ontology is one released version of the ontology.
type Ontology struct {
	version  string
	versions map[ID][]Node
	rels     []Relationship
}

// New validates an ontology release.
//
// Every version of one id keeps the id's family: an id that once named mobile
// voice cannot be repointed at broadband (ZTAX-CLS-REQ-0002). A semantic
// change is a new version with its own valid time, not an edit
// (ZTAX-CLS-REQ-0094), so versions of one id must not overlap in valid time.
func New(version string, nodes []Node, rels []Relationship) (*Ontology, error) {
	if version == "" {
		return nil, fmt.Errorf("ontology: release has no version")
	}
	o := &Ontology{version: version, versions: map[ID][]Node{}, rels: append([]Relationship(nil), rels...)}
	for _, n := range nodes {
		if err := n.validate(); err != nil {
			return nil, err
		}
		o.versions[n.ID] = append(o.versions[n.ID], n)
	}
	for nid, vs := range o.versions {
		sort.Slice(vs, func(i, j int) bool { return vs[i].Version < vs[j].Version })
		for i, v := range vs {
			if i > 0 {
				prev := vs[i-1]
				if v.Version == prev.Version {
					return nil, fmt.Errorf("ontology: %s v%d is declared twice", nid, v.Version)
				}
				if v.Family != prev.Family {
					return nil, fmt.Errorf("ontology: %s changes family from %s to %s; an id is never reused for another thing", nid, prev.Family, v.Family)
				}
				if prev.ValidTo == nil || prev.ValidTo.After(v.ValidFrom) {
					return nil, fmt.Errorf("ontology: %s v%d and v%d overlap in valid time", nid, prev.Version, v.Version)
				}
			}
		}
		for _, v := range vs {
			for _, r := range v.ReplacedBy {
				if _, ok := o.versions[r]; !ok {
					return nil, fmt.Errorf("ontology: %s is replaced by %s, which the release does not declare", nid, r)
				}
			}
		}
	}
	for _, r := range o.rels {
		if _, ok := o.versions[r.From]; !ok {
			return nil, fmt.Errorf("ontology: relationship from undeclared %s", r.From)
		}
		if _, ok := o.versions[r.To]; !ok {
			return nil, fmt.Errorf("ontology: relationship to undeclared %s", r.To)
		}
	}
	return o, nil
}

// Version returns the release version a classification pins.
func (o *Ontology) Version() string { return o.version }

// At returns the version of a node valid at t. A retired or split node still
// resolves for a time it was valid: history never loses the node it was
// classified against (ZTAX-CLS-REQ-0096, -0097).
func (o *Ontology) At(nodeID ID, t time.Time) (Node, bool) {
	for _, v := range o.versions[nodeID] {
		if v.ValidAt(t) {
			return v, true
		}
	}
	return Node{}, false
}

// Successors returns what a split or merged node became.
func (o *Ontology) Successors(nodeID ID) []ID {
	var out []ID
	for _, r := range o.rels {
		if r.From == nodeID && (r.Kind == RelSplitInto || r.Kind == RelMergedInto) {
			out = append(out, r.To)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Has reports whether the release declares a node at all.
func (o *Ontology) Has(nodeID ID) bool { _, ok := o.versions[nodeID]; return ok }

// ChargeType is how a component is charged, independent of what it is
// (ZTAX-CLS-REQ-0041 to -0044).
type ChargeType string

// The charge types.
const (
	ChargeRecurringAccess ChargeType = "RECURRING_ACCESS"
	ChargeUsage           ChargeType = "USAGE"
	ChargeActivation      ChargeType = "ACTIVATION"
	ChargeInstallation    ChargeType = "INSTALLATION"
	ChargeEquipment       ChargeType = "EQUIPMENT"
	ChargeNumberResource  ChargeType = "NUMBER_RESOURCE"
	ChargeTermination     ChargeType = "TERMINATION"
	ChargeOneTime         ChargeType = "ONE_TIME"
	// ChargeDiscount and ChargeCredit adjust other charges. They are never
	// classified as telecom services by themselves (ZTAX-CLS-REQ-0044).
	ChargeDiscount ChargeType = "DISCOUNT"
	ChargeCredit   ChargeType = "CREDIT"
)

// Adjusting reports whether a charge type adjusts another charge rather than
// being a supply of its own.
func (c ChargeType) Adjusting() bool { return c == ChargeDiscount || c == ChargeCredit }

// ResourceIdentity is a component's numbering and subscription identity:
// an E.164 number, an IMSI, an IP address. These are attributes of the
// resource, modelled apart from what the product is and from where the
// customer is (ZTAX-CLS-REQ-0037 to -0040). Nothing in this package reads
// them to classify, and nothing in package jurisdiction reads them as situs.
type ResourceIdentity struct {
	E164 string
	IMSI string
	IP   string
}

// ComponentInstance is one classified product component on one transaction:
// the canonical ServiceComponent it is, how it is charged, its qualifiers, and
// its resource identity.
type ComponentInstance struct {
	ID          string
	Node        ID
	NodeVersion int
	Charge      ChargeType
	Qualifiers  []Qualifier
	Identity    ResourceIdentity
	// Free marks a free or promotional component, still classified where it
	// is legally material (ZTAX-CLS-REQ-0052).
	Free bool
	// Adjusts names the instance a DISCOUNT or CREDIT charge adjusts. An
	// adjusting instance has no node of its own: it is not a supply, and it
	// takes its treatment from what it adjusts.
	Adjusts string
}

// Validate checks an instance against the ontology at an instant.
func (c ComponentInstance) Validate(o *Ontology, at time.Time) error {
	if c.ID == "" {
		return fmt.Errorf("ontology: component instance has no id")
	}
	if c.Charge.Adjusting() {
		// A discount pointing at a telecom node would be classified as the
		// telecom service it discounts (ZTAX-CLS-REQ-0044).
		if c.Node != "" || c.Adjusts == "" {
			return fmt.Errorf("ontology: %s charge %s must name the instance it adjusts and no node of its own", c.Charge, c.ID)
		}
		return nil
	}
	if c.Adjusts != "" {
		return fmt.Errorf("ontology: %s charge %s names an instance to adjust", c.Charge, c.ID)
	}
	n, ok := o.At(c.Node, at)
	if !ok {
		return fmt.Errorf("ontology: %s has no version valid at %s", c.Node, at.Format(time.RFC3339))
	}
	if n.Version != c.NodeVersion {
		return fmt.Errorf("ontology: instance %s pins %s v%d; v%d is the version valid then", c.ID, c.Node, c.NodeVersion, n.Version)
	}
	for _, q := range c.Qualifiers {
		if !q.Valid() {
			return fmt.Errorf("ontology: instance %s has qualifier %q", c.ID, q)
		}
	}
	return nil
}

// Families returns every family, sorted. The canonical schema's enum is
// checked against it (internal/schemas).
func Families() []Family { return sortedKeys(families) }

// Qualifiers returns every qualifier, sorted.
func Qualifiers() []Qualifier { return sortedKeys(qualifiers) }

// ChargeTypes returns every charge type, sorted.
func ChargeTypes() []ChargeType {
	return []ChargeType{
		ChargeActivation, ChargeCredit, ChargeDiscount, ChargeEquipment, ChargeInstallation,
		ChargeNumberResource, ChargeOneTime, ChargeRecurringAccess, ChargeTermination, ChargeUsage,
	}
}

func sortedKeys[K ~string](m map[K]bool) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
