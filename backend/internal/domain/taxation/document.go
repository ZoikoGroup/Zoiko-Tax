package taxation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/jurisdiction"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ontology"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Document is a telecom document as determination receives it: rated
// commercial facts, the parties, and the evidence of where the supply took
// place. It carries no tax: what is owed is the output.
//
// The tax point is not a field. It is the decision's event time, recorded in
// the envelope, so there is one instant the decision is about rather than two
// that could disagree.
type Document struct {
	// Type is INVOICE or CREDIT_NOTE. A credit note's amounts are negative.
	Type     string
	Currency fiscal.Currency
	// PriceMode is EXCLUSIVE (amounts are net) or INCLUSIVE (amounts include
	// the customer-charged taxes, which are extracted).
	PriceMode string
	Seller    Party
	Customer  Party
	// Evidence is document-level situs evidence (JUR-001 §3): the customer's
	// service address, billing address, SIM country and so on.
	Evidence     []Evidence
	Certificates []Certificate
	Lines        []Line
}

// Party is one party to the document.
type Party struct {
	// ID is the party's reference in the caller's systems. A certificate's
	// holder must match the customer's.
	ID string
	// Establishment is where the party is established for this supply.
	Establishment *Place
	// VAT is the customer's VAT identification, if supplied.
	VAT *VATIdentification
}

// Place names a location by its jurisdiction: exactly one of a jurisdiction
// identifier, an ISO 3166-2 subdivision code, or an ISO 3166-1 country code.
//
// There is no street address. Resolution never geocodes (JUR-001 §8.2): an
// address is mapped to a jurisdiction when it is recorded, by the system that
// owns it, and that mapping is what arrives here.
type Place struct {
	Jurisdiction string
	Subdivision  string
	Country      string
}

// VATIdentification is a VAT number's country and its validation status.
// The number itself is not needed to determine tax and is not retained.
type VATIdentification struct {
	Country    string
	Validation string
}

// Evidence is one situs evidence item as a caller supplies it.
type Evidence struct {
	Type   string
	Source string
	// CollectedAt is when the item was collected; absent, it is taken as
	// current at the tax point and recorded so.
	CollectedAt time.Time
	Place       Place
}

// Certificate is an exemption certificate (playbook §29). A customer-level
// "exempt" flag would be insufficient: what is exempt, where, for whom, when,
// and whether anyone verified it are all separate facts.
type Certificate struct {
	ID           string
	Type         string
	Holder       string
	Jurisdiction string
	// TaxTypes and Components scope the certificate; empty is unscoped on
	// that dimension.
	TaxTypes   []string
	Components []string
	// ValidFrom and ValidTo are dates; ValidTo is exclusive and optional.
	ValidFrom string
	ValidTo   string
	// Verification is VERIFIED, UNVERIFIED or UNAVAILABLE.
	Verification string
}

// Line is one rated line.
type Line struct {
	Key string
	// SKU is the caller's product code, kept for the trace. Classification
	// to an ontology family happens before determination; a line that
	// arrives without one is held for review rather than guessed at.
	SKU      string
	Service  string
	Amount   fiscal.Money
	Quantity *fiscal.Quantity
	Evidence []Evidence
	// Parts is an approved bundle decomposition: each part's service and its
	// allocated share. The parts must sum to the line amount exactly.
	Parts []Part
}

// Part is one component of a bundle line.
type Part struct {
	Key      string
	Service  string
	Amount   fiscal.Money
	Quantity *fiscal.Quantity
}

// The document vocabularies.
const (
	DocumentInvoice    = "INVOICE"
	DocumentCreditNote = "CREDIT_NOTE"
	PriceExclusive     = "EXCLUSIVE"
	PriceInclusive     = "INCLUSIVE"
	VerificationOK     = "VERIFIED"
	VerificationNone   = "UNVERIFIED"
	VerificationDown   = "UNAVAILABLE"
)

// Validate refuses a document whose shape is wrong. Thin evidence is not a
// shape problem — it is an outcome, decided by the engine and recorded.
func (d Document) Validate() error {
	switch d.Type {
	case DocumentInvoice, DocumentCreditNote:
	default:
		return fieldError("document.type", "is INVOICE or CREDIT_NOTE")
	}
	if !currencyCode(string(d.Currency)) {
		return fieldError("document.currency", "is an ISO 4217 alphabetic code")
	}
	switch d.PriceMode {
	case PriceExclusive, PriceInclusive:
	default:
		return fieldError("document.priceMode", "is EXCLUSIVE or INCLUSIVE")
	}
	if d.Seller.ID == "" || d.Seller.Establishment == nil {
		return fieldError("document.seller", "names the seller and where it is established")
	}
	if d.Seller.VAT != nil {
		return fieldError("document.seller.vat", "is not read; the seller's establishment is")
	}
	if err := d.Seller.Establishment.validate("document.seller.establishment"); err != nil {
		return err
	}
	if d.Customer.ID == "" {
		return fieldError("document.customer.id", "is required")
	}
	if d.Customer.Establishment != nil {
		if err := d.Customer.Establishment.validate("document.customer.establishment"); err != nil {
			return err
		}
	}
	if v := d.Customer.VAT; v != nil {
		if !countryCode(v.Country) || !jurisdiction.VATValidation(v.Validation).Valid() {
			return fieldError("document.customer.vat", "names a country and a validation status of VALIDATED, UNVALIDATED or VALIDATION_UNAVAILABLE")
		}
	}
	for i, e := range d.Evidence {
		if err := e.validate(fmt.Sprintf("document.evidence[%d]", i)); err != nil {
			return err
		}
	}
	ids := map[string]bool{}
	for i, c := range d.Certificates {
		if err := c.validate(fmt.Sprintf("document.certificates[%d]", i)); err != nil {
			return err
		}
		if ids[c.ID] {
			return fieldError("document.certificates.id", "is unique within the document")
		}
		ids[c.ID] = true
	}
	if len(d.Lines) == 0 {
		return fieldError("document.lines", "carries at least one line")
	}
	keys := map[string]bool{}
	for i, l := range d.Lines {
		if err := d.validateLine(i, l); err != nil {
			return err
		}
		if keys[l.Key] {
			return fieldError("document.lines.key", "is unique within the document")
		}
		keys[l.Key] = true
	}
	return nil
}

func (d Document) validateLine(i int, l Line) error {
	at := fmt.Sprintf("document.lines[%d]", i)
	if l.Key == "" || len(l.Key) > 64 {
		return fieldError(at+".key", "is required, at most 64 characters")
	}
	if err := d.validateAmount(at+".amount", l.Amount); err != nil {
		return err
	}
	if l.Service != "" && !ontology.Family(l.Service).Valid() {
		return fieldError(at+".service", "is an ontology family, such as MOBILE_VOICE")
	}
	for j, e := range l.Evidence {
		if err := e.validate(fmt.Sprintf("%s.evidence[%d]", at, j)); err != nil {
			return err
		}
	}
	if len(l.Parts) == 0 {
		return nil
	}
	if l.Service != "" || l.Quantity != nil {
		return fieldError(at+".parts", "replace the line's own service and quantity; a bundle line carries neither")
	}
	sum := l.Amount.Zero()
	parts := map[string]bool{}
	for j, p := range l.Parts {
		pat := fmt.Sprintf("%s.parts[%d]", at, j)
		if p.Key == "" || len(p.Key) > 64 || parts[p.Key] {
			return fieldError(pat+".key", "is required, unique within the line and at most 64 characters")
		}
		parts[p.Key] = true
		if p.Service != "" && !ontology.Family(p.Service).Valid() {
			return fieldError(pat+".service", "is an ontology family, such as MOBILE_DATA")
		}
		if err := d.validateAmount(pat+".amount", p.Amount); err != nil {
			return err
		}
		var err error
		if sum, err = sum.Add(p.Amount); err != nil {
			return fieldError(pat+".amount", "cannot be summed")
		}
	}
	if c, err := sum.Cmp(l.Amount); err != nil || c != 0 {
		// The allocation is approved upstream (playbook §7: "allocation
		// total is exactly 100.00"). A residual here is a defect in it,
		// and absorbing the residual would be an allocation nobody approved.
		return fieldError(at+".parts", fmt.Sprintf("sum to %s; the line is %s, and an allocation must account for it exactly",
			sum.CanonicalString(), l.Amount.CanonicalString()))
	}
	return nil
}

func (d Document) validateAmount(field string, m fiscal.Money) error {
	if m.Currency() != d.Currency {
		return fieldError(field, "is in the document currency")
	}
	if d.Type == DocumentInvoice && m.Sign() < 0 {
		return fieldError(field, "is not negative on an invoice; a credit is a CREDIT_NOTE")
	}
	if d.Type == DocumentCreditNote && m.Sign() > 0 {
		return fieldError(field, "is not positive on a credit note")
	}
	return nil
}

func (p Place) validate(field string) error {
	set := 0
	if p.Jurisdiction != "" {
		set++
		if err := jurisdiction.ID(p.Jurisdiction).Validate(); err != nil {
			return fieldError(field+".jurisdiction", "follows the jurisdiction: grammar")
		}
	}
	if p.Subdivision != "" {
		set++
		if len(p.Subdivision) < 4 || p.Subdivision[2] != '-' || !countryCode(p.Subdivision[:2]) {
			return fieldError(field+".subdivision", "is an ISO 3166-2 code, such as CA-QC")
		}
	}
	if p.Country != "" {
		set++
		if !countryCode(p.Country) {
			return fieldError(field+".country", "is an ISO 3166-1 alpha-2 code")
		}
	}
	if set != 1 {
		return fieldError(field, "names exactly one of a jurisdiction, a subdivision or a country")
	}
	return nil
}

func (e Evidence) validate(field string) error {
	t := jurisdiction.EvidenceType(e.Type)
	if !t.Valid() {
		return fieldError(field+".type", "is a JUR-001 evidence type, such as SERVICE_ADDRESS")
	}
	switch t {
	case jurisdiction.EvidenceVATIdentification, jurisdiction.EvidenceSellerEstablishment,
		jurisdiction.EvidenceCustomerEstablishment, jurisdiction.EvidenceDeviceCoordinates:
		// The first three have their own fields on the parties, so a second
		// route in would let one fact arrive twice and disagree with itself.
		// Coordinates need a boundary dataset the profile does not carry.
		return fieldError(field+".type", "is supplied through the parties, or is not supported as evidence here")
	}
	if !jurisdiction.EvidenceSource(e.Source).Valid() {
		return fieldError(field+".source", "is a JUR-001 evidence source, such as SELLER_RECORDED")
	}
	return e.Place.validate(field + ".place")
}

func (c Certificate) validate(field string) error {
	switch {
	case c.ID == "" || c.Type == "" || c.Holder == "":
		return fieldError(field, "names its id, type and holder")
	case jurisdiction.ID(c.Jurisdiction).Validate() != nil:
		return fieldError(field+".jurisdiction", "names the jurisdiction that issued it")
	}
	if _, err := time.Parse(time.DateOnly, c.ValidFrom); err != nil {
		return fieldError(field+".validFrom", "is a YYYY-MM-DD date")
	}
	if c.ValidTo != "" {
		if _, err := time.Parse(time.DateOnly, c.ValidTo); err != nil {
			return fieldError(field+".validTo", "is a YYYY-MM-DD date")
		}
	}
	switch c.Verification {
	case VerificationOK, VerificationNone, VerificationDown:
	default:
		return fieldError(field+".verification", "is VERIFIED, UNVERIFIED or UNAVAILABLE")
	}
	return nil
}

// FieldError is a document shape problem, with the field it concerns.
type FieldError struct {
	Field  string
	Detail string
}

func (e *FieldError) Error() string { return e.Field + " " + e.Detail }

func fieldError(field, detail string) error { return &FieldError{Field: field, Detail: detail} }

func countryCode(s string) bool {
	if len(s) != 2 {
		return false
	}
	return s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z'
}

func currencyCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// canonical form
// ---------------------------------------------------------------------------

// Canonical renders the document for the envelope and the request digest.
// Array order is the caller's and is significant (ADR-0011 P4): line order is
// the allocation tie-break and the trace order.
func (d Document) Canonical() canonical.Value {
	return canonical.Object(
		canonical.F("type", canonical.String(d.Type)),
		canonical.F("currency", canonical.String(string(d.Currency))),
		canonical.F("priceMode", canonical.String(d.PriceMode)),
		canonical.F("seller", d.Seller.canonical()),
		canonical.F("customer", d.Customer.canonical()),
		canonical.F("evidence", evidenceArray(d.Evidence)),
		canonical.F("certificates", certificateArray(d.Certificates)),
		canonical.F("lines", lineArray(d.Lines)),
	)
}

func (p Party) canonical() canonical.Value {
	est := canonical.Absent()
	if p.Establishment != nil {
		est = p.Establishment.canonical()
	}
	vat := canonical.Absent()
	if p.VAT != nil {
		vat = canonical.Object(
			canonical.F("country", canonical.String(p.VAT.Country)),
			canonical.F("validation", canonical.String(p.VAT.Validation)),
		)
	}
	return canonical.Object(
		canonical.F("id", canonical.String(p.ID)),
		canonical.F("establishment", est),
		canonical.F("vat", vat),
	)
}

func (p Place) canonical() canonical.Value {
	return canonical.Object(
		canonical.F("jurisdiction", canonical.OptString(p.Jurisdiction)),
		canonical.F("subdivision", canonical.OptString(p.Subdivision)),
		canonical.F("country", canonical.OptString(p.Country)),
	)
}

func evidenceArray(es []Evidence) canonical.Value {
	if len(es) == 0 {
		return canonical.Absent()
	}
	out := make([]canonical.Value, len(es))
	for i, e := range es {
		at := canonical.Absent()
		if !e.CollectedAt.IsZero() {
			at = canonical.Time(e.CollectedAt)
		}
		out[i] = canonical.Object(
			canonical.F("type", canonical.String(e.Type)),
			canonical.F("source", canonical.String(e.Source)),
			canonical.F("collectedAt", at),
			canonical.F("place", e.Place.canonical()),
		)
	}
	return canonical.Array(out...)
}

func certificateArray(cs []Certificate) canonical.Value {
	if len(cs) == 0 {
		return canonical.Absent()
	}
	out := make([]canonical.Value, len(cs))
	for i, c := range cs {
		out[i] = canonical.Object(
			canonical.F("id", canonical.String(c.ID)),
			canonical.F("type", canonical.String(c.Type)),
			canonical.F("holder", canonical.String(c.Holder)),
			canonical.F("jurisdiction", canonical.String(c.Jurisdiction)),
			canonical.F("taxTypes", stringArray(c.TaxTypes)),
			canonical.F("components", stringArray(c.Components)),
			canonical.F("validFrom", canonical.String(c.ValidFrom)),
			canonical.F("validTo", canonical.OptString(c.ValidTo)),
			canonical.F("verification", canonical.String(c.Verification)),
		)
	}
	return canonical.Array(out...)
}

func lineArray(ls []Line) canonical.Value {
	out := make([]canonical.Value, len(ls))
	for i, l := range ls {
		parts := canonical.Absent()
		if len(l.Parts) > 0 {
			ps := make([]canonical.Value, len(l.Parts))
			for j, p := range l.Parts {
				ps[j] = canonical.Object(
					canonical.F("key", canonical.String(p.Key)),
					canonical.F("service", canonical.OptString(p.Service)),
					canonical.F("amount", canonical.Money(p.Amount)),
					canonical.F("quantity", quantityValue(p.Quantity)),
				)
			}
			parts = canonical.Array(ps...)
		}
		out[i] = canonical.Object(
			canonical.F("key", canonical.String(l.Key)),
			canonical.F("sku", canonical.OptString(l.SKU)),
			canonical.F("service", canonical.OptString(l.Service)),
			canonical.F("amount", canonical.Money(l.Amount)),
			canonical.F("quantity", quantityValue(l.Quantity)),
			canonical.F("evidence", evidenceArray(l.Evidence)),
			canonical.F("parts", parts),
		)
	}
	return canonical.Array(out...)
}

func quantityValue(q *fiscal.Quantity) canonical.Value {
	if q == nil {
		return canonical.Absent()
	}
	return canonical.Object(
		canonical.F("value", canonical.Quantity(*q)),
		canonical.F("unit", canonical.String(string(q.Unit()))),
	)
}

func stringArray(ss []string) canonical.Value {
	if len(ss) == 0 {
		return canonical.Absent()
	}
	out := make([]canonical.Value, len(ss))
	for i, s := range ss {
		out[i] = canonical.String(s)
	}
	return canonical.Array(out...)
}

// ---------------------------------------------------------------------------
// decoding
// ---------------------------------------------------------------------------

// DecodeDocument reads a document from its canonical form, strictly: unknown
// members are refused, and the decoded document must re-encode to the input
// byte for byte (ADR-0011 P5 and the evidence codec's round-trip rule).
func DecodeDocument(data []byte) (Document, error) {
	var w documentWire
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return Document{}, fmt.Errorf("taxation: decode document: %w", err)
	}
	d, err := w.decode()
	if err != nil {
		return Document{}, err
	}
	again, err := canonical.Encode(d.Canonical())
	if err != nil {
		return Document{}, err
	}
	if !bytes.Equal(again, data) {
		return Document{}, fmt.Errorf("taxation: document is not in %s form", canonical.ProfileVersion)
	}
	return d, nil
}

// DocumentWire is the document's JSON form, shared by the evidence decoder
// and, through the same field names, the transport.
type documentWire struct {
	Type         string            `json:"type"`
	Currency     string            `json:"currency"`
	PriceMode    string            `json:"priceMode"`
	Seller       partyWire         `json:"seller"`
	Customer     partyWire         `json:"customer"`
	Evidence     []evidenceWire    `json:"evidence"`
	Certificates []certificateWire `json:"certificates"`
	Lines        []lineWire        `json:"lines"`
}

type partyWire struct {
	ID            string     `json:"id"`
	Establishment *placeWire `json:"establishment"`
	VAT           *struct {
		Country    string `json:"country"`
		Validation string `json:"validation"`
	} `json:"vat"`
}

type placeWire struct {
	Jurisdiction string `json:"jurisdiction"`
	Subdivision  string `json:"subdivision"`
	Country      string `json:"country"`
}

type evidenceWire struct {
	Type        string    `json:"type"`
	Source      string    `json:"source"`
	CollectedAt string    `json:"collectedAt"`
	Place       placeWire `json:"place"`
}

type certificateWire struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Holder       string   `json:"holder"`
	Jurisdiction string   `json:"jurisdiction"`
	TaxTypes     []string `json:"taxTypes"`
	Components   []string `json:"components"`
	ValidFrom    string   `json:"validFrom"`
	ValidTo      string   `json:"validTo"`
	Verification string   `json:"verification"`
}

type quantityWire struct {
	Value string `json:"value"`
	Unit  string `json:"unit"`
}

type lineWire struct {
	Key      string         `json:"key"`
	SKU      string         `json:"sku"`
	Service  string         `json:"service"`
	Amount   string         `json:"amount"`
	Quantity *quantityWire  `json:"quantity"`
	Evidence []evidenceWire `json:"evidence"`
	Parts    []struct {
		Key      string        `json:"key"`
		Service  string        `json:"service"`
		Amount   string        `json:"amount"`
		Quantity *quantityWire `json:"quantity"`
	} `json:"parts"`
}

func (w documentWire) decode() (Document, error) {
	cur := fiscal.Currency(w.Currency)
	d := Document{
		Type: w.Type, Currency: cur, PriceMode: w.PriceMode,
		Seller: w.Seller.decode(), Customer: w.Customer.decode(),
	}
	var err error
	if d.Evidence, err = decodeEvidence(w.Evidence); err != nil {
		return Document{}, err
	}
	for _, c := range w.Certificates {
		d.Certificates = append(d.Certificates, Certificate(c))
	}
	for _, lw := range w.Lines {
		l := Line{Key: lw.Key, SKU: lw.SKU, Service: lw.Service}
		if l.Amount, err = fiscal.ParseMoney(lw.Amount, cur); err != nil {
			return Document{}, fmt.Errorf("taxation: line %s amount: %w", lw.Key, err)
		}
		if l.Quantity, err = decodeQuantity(lw.Quantity); err != nil {
			return Document{}, fmt.Errorf("taxation: line %s quantity: %w", lw.Key, err)
		}
		if l.Evidence, err = decodeEvidence(lw.Evidence); err != nil {
			return Document{}, err
		}
		for _, pw := range lw.Parts {
			p := Part{Key: pw.Key, Service: pw.Service}
			if p.Amount, err = fiscal.ParseMoney(pw.Amount, cur); err != nil {
				return Document{}, fmt.Errorf("taxation: line %s part %s amount: %w", lw.Key, pw.Key, err)
			}
			if p.Quantity, err = decodeQuantity(pw.Quantity); err != nil {
				return Document{}, fmt.Errorf("taxation: line %s part %s quantity: %w", lw.Key, pw.Key, err)
			}
			l.Parts = append(l.Parts, p)
		}
		d.Lines = append(d.Lines, l)
	}
	return d, nil
}

func (w partyWire) decode() Party {
	p := Party{ID: w.ID}
	if w.Establishment != nil {
		pl := w.Establishment.decode()
		p.Establishment = &pl
	}
	if w.VAT != nil {
		p.VAT = &VATIdentification{Country: w.VAT.Country, Validation: w.VAT.Validation}
	}
	return p
}

func (w placeWire) decode() Place {
	return Place(w)
}

func decodeEvidence(ws []evidenceWire) ([]Evidence, error) {
	var out []Evidence
	for _, w := range ws {
		e := Evidence{Type: w.Type, Source: w.Source, Place: w.Place.decode()}
		if w.CollectedAt != "" {
			t, err := time.Parse("2006-01-02T15:04:05.000000Z", w.CollectedAt)
			if err != nil {
				return nil, fmt.Errorf("taxation: evidence collectedAt %q is not a canon/v1 instant", w.CollectedAt)
			}
			e.CollectedAt = t.UTC()
		}
		out = append(out, e)
	}
	return out, nil
}

func decodeQuantity(w *quantityWire) (*fiscal.Quantity, error) {
	if w == nil {
		return nil, nil
	}
	q, err := fiscal.ParseQuantity(w.Value, fiscal.Unit(w.Unit))
	if err != nil {
		return nil, err
	}
	return &q, nil
}
