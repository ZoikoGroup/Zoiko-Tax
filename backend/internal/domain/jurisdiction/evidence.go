package jurisdiction

import (
	"bytes"
	"fmt"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// EvidenceType is the closed vocabulary of JUR-001 §3.2. Adding a member is a
// SCHEMA train change with a new golden-vector set, because a pack that could
// invent a type no other pack understands would make precedence compare
// incommensurable things (JUR-REQ-0006).
type EvidenceType string

// The evidence types, in declaration order. That order is also the canonical
// order evidence is sorted into before resolution (JUR-REQ-0026); it carries
// no precedence, which is always the situs rule's.
const (
	EvidencePrimaryPlaceOfUse     EvidenceType = "PRIMARY_PLACE_OF_USE"
	EvidenceServiceAddress        EvidenceType = "SERVICE_ADDRESS"
	EvidenceBillingAddress        EvidenceType = "BILLING_ADDRESS"
	EvidenceFixedLineLocation     EvidenceType = "FIXED_LINE_LOCATION"
	EvidenceDeviceCoordinates     EvidenceType = "DEVICE_COORDINATES"
	EvidenceNetworkLocation       EvidenceType = "NETWORK_LOCATION"
	EvidenceSIMCountry            EvidenceType = "SIM_COUNTRY"
	EvidenceIPGeolocation         EvidenceType = "IP_GEOLOCATION"
	EvidenceBankLocation          EvidenceType = "BANK_LOCATION"
	EvidenceCustomerDeclaration   EvidenceType = "CUSTOMER_DECLARATION"
	EvidenceSellerEstablishment   EvidenceType = "SELLER_ESTABLISHMENT"
	EvidenceCustomerEstablishment EvidenceType = "CUSTOMER_ESTABLISHMENT"
	EvidenceVATIdentification     EvidenceType = "VAT_IDENTIFICATION"
)

var evidenceOrder = map[EvidenceType]int{
	EvidencePrimaryPlaceOfUse: 0, EvidenceServiceAddress: 1, EvidenceBillingAddress: 2,
	EvidenceFixedLineLocation: 3, EvidenceDeviceCoordinates: 4, EvidenceNetworkLocation: 5,
	EvidenceSIMCountry: 6, EvidenceIPGeolocation: 7, EvidenceBankLocation: 8,
	EvidenceCustomerDeclaration: 9, EvidenceSellerEstablishment: 10,
	EvidenceCustomerEstablishment: 11, EvidenceVATIdentification: 12,
}

// Valid reports whether t is in the closed vocabulary.
func (t EvidenceType) Valid() bool { _, ok := evidenceOrder[t]; return ok }

// EvidenceSource is who asserted an item (JUR-001 §3.3).
type EvidenceSource string

// The sources.
const (
	SourceCustomerAsserted   EvidenceSource = "CUSTOMER_ASSERTED"
	SourceSellerRecorded     EvidenceSource = "SELLER_RECORDED"
	SourceNetworkDerived     EvidenceSource = "NETWORK_DERIVED"
	SourceThirdPartyVerified EvidenceSource = "THIRD_PARTY_VERIFIED"
	SourceAuthorityVerified  EvidenceSource = "AUTHORITY_VERIFIED"
)

// Valid reports whether s is a known source.
func (s EvidenceSource) Valid() bool {
	switch s {
	case SourceCustomerAsserted, SourceSellerRecorded, SourceNetworkDerived, SourceThirdPartyVerified, SourceAuthorityVerified:
		return true
	}
	return false
}

// ReliabilityClass is how much a situs rule trusts an evidence type. It is
// assigned by the rule, never globally (JUR-001 §3.4, JUR-REQ-0007).
type ReliabilityClass string

// The classes.
const (
	ReliabilityStrong     ReliabilityClass = "STRONG"
	ReliabilitySupporting ReliabilityClass = "SUPPORTING"
	ReliabilityWeak       ReliabilityClass = "WEAK"
)

var reliabilityRank = map[ReliabilityClass]int{ReliabilityWeak: 1, ReliabilitySupporting: 2, ReliabilityStrong: 3}

// Valid reports whether c is a known class.
func (c ReliabilityClass) Valid() bool { _, ok := reliabilityRank[c]; return ok }

// AtLeast reports whether c is at or above min.
func (c ReliabilityClass) AtLeast(min ReliabilityClass) bool {
	return reliabilityRank[c] >= reliabilityRank[min]
}

// VATValidation is the validation status of a VAT identification. The three
// are different facts and a pack may treat them differently (JUR-001 §7.3,
// JUR-REQ-0021): "unvalidated" is a number nobody checked, and "validation
// unavailable" is a number we tried to check and could not.
type VATValidation string

// The statuses.
const (
	VATValidated             VATValidation = "VALIDATED"
	VATUnvalidated           VATValidation = "UNVALIDATED"
	VATValidationUnavailable VATValidation = "VALIDATION_UNAVAILABLE"
)

// Valid reports whether v is a known status.
func (v VATValidation) Valid() bool {
	return v == VATValidated || v == VATUnvalidated || v == VATValidationUnavailable
}

// SitusEvidence is one typed, attributed signal about where a transaction
// occurred or a service is used (JUR-001 §3.1).
//
// Exactly one of Jurisdiction, Point and Country carries its value:
//
//   - Jurisdiction for address evidence, mapped to a graph node when it was
//     recorded (§5.2) — resolution never geocodes (§8.2);
//   - Point for coordinates;
//   - Country for evidence that only ever says a country: a SIM's MCC, an IP
//     country, a payment instrument's country, a VAT number's prefix.
//
// An absent item is absent. Nothing here substitutes one field for another
// (JUR-REQ-0009): a missing service address is not a billing address.
type SitusEvidence struct {
	Type          EvidenceType
	Source        EvidenceSource
	CollectedAt   time.Time
	Jurisdiction  ID
	Point         *Point
	Country       string
	VATValidation VATValidation
}

// Validate refuses an item that is not well-formed.
func (e SitusEvidence) Validate() error {
	if !e.Type.Valid() {
		return fmt.Errorf("jurisdiction: evidence type %q is not in the closed vocabulary", e.Type)
	}
	if !e.Source.Valid() {
		return fmt.Errorf("jurisdiction: %s evidence has source %q", e.Type, e.Source)
	}
	if e.CollectedAt.IsZero() {
		return fmt.Errorf("jurisdiction: %s evidence has no collection time", e.Type)
	}
	set := 0
	if e.Jurisdiction != "" {
		set++
		if err := e.Jurisdiction.Validate(); err != nil {
			return err
		}
	}
	if e.Point != nil {
		set++
		if err := e.Point.Validate(); err != nil {
			return err
		}
	}
	if e.Country != "" {
		set++
		if !countryCode(e.Country) {
			return fmt.Errorf("jurisdiction: %s evidence has country %q", e.Type, e.Country)
		}
	}
	if set != 1 {
		return fmt.Errorf("jurisdiction: %s evidence must carry exactly one of a jurisdiction, a point or a country; it carries %d", e.Type, set)
	}
	if e.Type == EvidenceVATIdentification {
		if !e.VATValidation.Valid() {
			return fmt.Errorf("jurisdiction: VAT identification evidence has validation status %q", e.VATValidation)
		}
	} else if e.VATValidation != "" {
		return fmt.Errorf("jurisdiction: %s evidence carries a VAT validation status", e.Type)
	}
	return nil
}

// Canonical renders an item for evidence and for sorting.
func (e SitusEvidence) Canonical() canonical.Value {
	point := canonical.Absent()
	if e.Point != nil {
		point = e.Point.Canonical()
	}
	return canonical.Object(
		canonical.F("type", canonical.String(string(e.Type))),
		canonical.F("source", canonical.String(string(e.Source))),
		canonical.F("collectedAt", canonical.Time(e.CollectedAt)),
		canonical.F("jurisdiction", canonical.OptString(string(e.Jurisdiction))),
		canonical.F("point", point),
		canonical.F("country", canonical.OptString(e.Country)),
		canonical.F("vatValidation", canonical.OptString(string(e.VATValidation))),
	)
}

// canonicalise validates every item and orders them by type, then by their
// canonical bytes, so the order a caller supplied them in cannot reach the
// result (JUR-001 §10.4, JUR-REQ-0026).
func canonicalise(items []SitusEvidence) ([]SitusEvidence, error) {
	type keyed struct {
		e   SitusEvidence
		key []byte
	}
	ks := make([]keyed, len(items))
	for i, e := range items {
		if err := e.Validate(); err != nil {
			return nil, err
		}
		b, err := canonical.Encode(e.Canonical())
		if err != nil {
			return nil, err
		}
		ks[i] = keyed{e, b}
	}
	sort.SliceStable(ks, func(i, j int) bool {
		oi, oj := evidenceOrder[ks[i].e.Type], evidenceOrder[ks[j].e.Type]
		if oi != oj {
			return oi < oj
		}
		return bytes.Compare(ks[i].key, ks[j].key) < 0
	})
	out := make([]SitusEvidence, len(ks))
	for i, k := range ks {
		out[i] = k.e
	}
	return out, nil
}
