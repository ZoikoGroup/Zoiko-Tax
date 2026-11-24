// Package crosswalk models vendor crosswalks: versioned mappings from an
// external vendor's codes — an incumbent tax engine's product or transaction
// codes, a BSS's tax codes — onto Zoiko canonical service codes, with an
// optional levy hint (Engineering Doc 02 §03 "VXC", WF-06 Vendor Crosswalk
// Library, WF-07 Crosswalk Composer; Doc 12 FED-04; Doc 03 §"Vendor crosswalk
// is semantic mapping with permission check, not replicated proprietary legal
// data").
//
// Three rules shape it.
//
//   - A crosswalk is never fiscal authority. It translates an outside code into
//     a canonical service code a person can then classify with; it never
//     determines taxability, a levy or a rate (Doc 02 §02 "Vendor code replaces
//     SCC/TLC/JRI" is the forbidden shortcut; Doc 12 "Imported vendor tax codes
//     are reference keys, never automatically native TLC/JRI authority").
//     Every lookup answers ADVISORY, and nothing here is read by the
//     determination engine.
//   - Vendor code data is someone else's property. Every version names the
//     basis on which Zoiko may hold it — the vendor licence, the customer's own
//     configuration or the vendor's public API documentation — and the
//     reference to that grant, and a lapsed grant quarantines the mapping
//     rather than letting it serve (Doc 02 WF-06 "expired license -> vendor
//     adapter response quarantined"; ZTAX-SRC-001 §"Transforming a vendor
//     database into a Zoiko schema does not automatically remove underlying
//     database/licence restrictions").
//   - A version is immutable from the moment it is written. Its entries never
//     change; its status is the fold of appended events; a correction is the
//     next version, and approving it supersedes the one before, which stays
//     readable so a historical translation can be reproduced (Doc 02 §01
//     "Version every classification, predicate, crosswalk ...").
//
// Approval is four-eyes: a person other than the version's author and its
// submitter approves the exact version they read (Doc 02 §07 "self-approval
// denied"; WF-07 "two-reviewer approval").
package crosswalk

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/classification"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/registration"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/remittance"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// The reason codes (Doc 02 WF-07's conflict classes).
var (
	// ReasonUnresolvedAmbiguity is one external code mapped twice without a
	// predicate that tells the two apart, or a partial match with no
	// predicate at all (Doc 02 WF-07 UNRESOLVED_AMBIGUITY: "Add
	// discriminating predicate or BLOCK").
	ReasonUnresolvedAmbiguity = errs.Register("CROSSWALK_UNRESOLVED_AMBIGUITY", errs.CategoryValidation,
		"content-crosswalk",
		"An external code is mapped more than once without a discriminating predicate, or a partial match names no predicate. An ambiguous mapping is blocked, never guessed: add the predicate that tells the targets apart.")

	// ReasonRightsLapsed is a crosswalk whose vendor-data grant has ended
	// (Doc 02 WF-06, Z02-AT-13 "Unlicensed vendor dataset: publication
	// blocked").
	ReasonRightsLapsed = errs.Register("CROSSWALK_RIGHTS_LAPSED", errs.CategoryPolicy,
		"content-crosswalk",
		"The grant under which this vendor's code data is held has ended. The crosswalk cannot be approved or reinstated until a version records a current licence or customer authorization.")

	// ReasonVersionConflict is an act on a revision that is no longer
	// current.
	ReasonVersionConflict = registration.ReasonVersionConflict

	// ReasonMakerChecker is the estate's four-eyes code.
	ReasonMakerChecker = remittance.ReasonMakerChecker
)

// Bounds.
const (
	MaxEntries     = 2000
	MaxCode        = 128
	MaxText        = 200
	MaxDescription = 500
	MaxReason      = 2000
)

var (
	nameShape = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_.-]{0,63}$`)
	levyShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)
	sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Match is how well one external code corresponds to the canonical target.
type Match string

// The match qualities.
const (
	// MatchExact: the external code means exactly the canonical service.
	MatchExact Match = "EXACT"
	// MatchPartial: it means the canonical service only under a predicate —
	// a service feature, commercial role or billing event the code itself
	// does not carry (Doc 02 WF-07 "IF exact vendor code ... -> select SCC").
	MatchPartial Match = "PARTIAL"
	// MatchNoEquivalent: there is no canonical service it corresponds to.
	// Recorded so that "we looked and it maps to nothing" is distinct from
	// "nobody mapped it" (FED-04 UNMAPPED).
	MatchNoEquivalent Match = "NO_EQUIVALENT"
)

// Valid reports whether m is a known match quality.
func (m Match) Valid() bool { return m == MatchExact || m == MatchPartial || m == MatchNoEquivalent }

// RightsBasis is on what footing Zoiko holds the vendor's code data (Doc 02
// WF-06 "Evidence: vendor contract/right, publicly documented API field
// semantics, customer-supplied configuration").
type RightsBasis string

// The rights bases.
const (
	RightsVendorLicence       RightsBasis = "VENDOR_LICENCE"
	RightsCustomerSupplied    RightsBasis = "CUSTOMER_SUPPLIED"
	RightsPublicDocumentation RightsBasis = "PUBLIC_DOCUMENTATION"
)

// Valid reports whether b is a known basis.
func (b RightsBasis) Valid() bool {
	return b == RightsVendorLicence || b == RightsCustomerSupplied || b == RightsPublicDocumentation
}

// Rights is the grant a version's vendor data is held under.
type Rights struct {
	Basis RightsBasis
	// Reference names the grant: a contract or order-form number, the
	// customer's written authorization, the documentation URL and edition.
	Reference string
	// ExpiresOn is the last day the grant covers; zero for no end date.
	ExpiresOn time.Time
	// EvidenceSHA256 is the digest of the grant document, hashed in the
	// browser and never uploaded. Optional.
	EvidenceSHA256 string
}

// LapsedOn reports whether the grant has ended by day.
func (r Rights) LapsedOn(day time.Time) bool { return !r.ExpiresOn.IsZero() && day.After(r.ExpiresOn) }

// Validate checks the grant is named.
func (r Rights) Validate() error {
	if !r.Basis.Valid() {
		return errs.Invalid("rights.basis", errs.ReasonInvalidValue,
			"The rights basis is VENDOR_LICENCE, CUSTOMER_SUPPLIED or PUBLIC_DOCUMENTATION. Vendor code data without a recorded basis is not held.")
	}
	if err := text("rights.reference", r.Reference, true, MaxText); err != nil {
		return err
	}
	if r.EvidenceSHA256 != "" && !sha256Hex.MatchString(r.EvidenceSHA256) {
		return errs.Invalid("rights.evidenceSha256", errs.ReasonInvalidValue, "The grant document digest is its SHA-256 in lowercase hex.")
	}
	return nil
}

// Entry is one row of a crosswalk version: an external code and what it
// corresponds to.
type Entry struct {
	// ExternalCode is the vendor's code exactly as the vendor emits it.
	// Lookup is exact: no case folding, no prefix, no wildcard (Doc 02 WF-06
	// "Search external code (exact only)", "No wildcard active fallback").
	ExternalCode string
	// Description is the vendor's own meaning of the code, preserved so the
	// translation round-trips to the original semantics (WF-07).
	Description string
	Match       Match
	// ServiceTarget is the canonical service code. Required for EXACT and
	// PARTIAL, absent for NO_EQUIVALENT.
	ServiceTarget classification.OntologyID
	// LevyHint is an optional candidate levy component code. It is a hint
	// for a content researcher and never creates a levy (Doc 02 WF-07
	// SEMANTIC_MISMATCH "Keep vendor-specific metadata; no auto levy").
	LevyHint string
	// Predicate is the condition under which the code means the target.
	// Required for PARTIAL, and for every entry of a code mapped more than
	// once.
	Predicate string
	Note      string
}

// Normalize trims what a person typed.
func (e Entry) Normalize() Entry {
	e.ExternalCode = strings.TrimSpace(e.ExternalCode)
	e.Description = strings.TrimSpace(e.Description)
	e.ServiceTarget = classification.OntologyID(strings.TrimSpace(string(e.ServiceTarget)))
	e.LevyHint = strings.TrimSpace(e.LevyHint)
	e.Predicate = strings.TrimSpace(e.Predicate)
	e.Note = strings.TrimSpace(e.Note)
	return e
}

func (e Entry) validate(i int) error {
	f := func(name string) string { return fmt.Sprintf("entries[%d].%s", i, name) }
	if err := text(f("externalCode"), e.ExternalCode, true, MaxCode); err != nil {
		return err
	}
	if err := text(f("description"), e.Description, true, MaxDescription); err != nil {
		return err
	}
	if !e.Match.Valid() {
		return errs.Invalid(f("match"), errs.ReasonInvalidValue, "The match is EXACT, PARTIAL or NO_EQUIVALENT.")
	}
	if e.Match == MatchNoEquivalent {
		if e.ServiceTarget != "" || e.LevyHint != "" {
			return errs.Invalid(f("serviceTarget"), errs.ReasonInvalidValue, "A code with no equivalent names no target and no levy hint.")
		}
	} else if err := e.ServiceTarget.Validate(); err != nil {
		return errs.Invalid(f("serviceTarget"), errs.ReasonInvalidValue,
			"An EXACT or PARTIAL match names a canonical service code (ontology:...).")
	}
	if e.LevyHint != "" && !levyShape.MatchString(e.LevyHint) {
		return errs.Invalid(f("levyHint"), errs.ReasonInvalidValue, "A levy hint is a content component code.")
	}
	if e.Match == MatchPartial && e.Predicate == "" {
		return errs.New(errs.CategoryValidation, ReasonUnresolvedAmbiguity,
			fmt.Sprintf("Entry %d (%s) is a PARTIAL match with no predicate. Say under what condition the code means the target.", i, e.ExternalCode))
	}
	if err := text(f("predicate"), e.Predicate, false, MaxDescription); err != nil {
		return err
	}
	return text(f("note"), e.Note, false, MaxDescription)
}

// CheckEntries validates the entries of a version and refuses ambiguity: the
// same code twice with the same predicate, or a code mapped more than once
// where one of its entries carries no predicate (WF-07, Z02-AT-11).
func CheckEntries(entries []Entry) error {
	if len(entries) == 0 {
		return errs.Invalid("entries", errs.ReasonMissingField, "A crosswalk version maps at least one external code.")
	}
	if len(entries) > MaxEntries {
		return errs.Invalid("entries", errs.ReasonInvalidValue, fmt.Sprintf("A crosswalk version maps at most %d codes.", MaxEntries))
	}
	byCode := map[string][]Entry{}
	for i, e := range entries {
		if err := e.validate(i); err != nil {
			return err
		}
		byCode[e.ExternalCode] = append(byCode[e.ExternalCode], e)
	}
	for code, es := range byCode {
		if len(es) < 2 {
			continue
		}
		seen := map[string]bool{}
		for _, e := range es {
			if e.Predicate == "" {
				return errs.New(errs.CategoryValidation, ReasonUnresolvedAmbiguity,
					fmt.Sprintf("External code %s is mapped %d times and one entry has no predicate. Every entry of a code mapped more than once says when it applies.", code, len(es)))
			}
			if seen[e.Predicate] {
				return errs.New(errs.CategoryValidation, ReasonUnresolvedAmbiguity,
					fmt.Sprintf("External code %s is mapped twice under the same predicate.", code))
			}
			seen[e.Predicate] = true
		}
	}
	return nil
}

// SortEntries orders entries by code, then predicate: the canonical order
// a version's digest is taken in.
func SortEntries(entries []Entry) []Entry {
	out := append([]Entry(nil), entries...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ExternalCode != out[j].ExternalCode {
			return out[i].ExternalCode < out[j].ExternalCode
		}
		return out[i].Predicate < out[j].Predicate
	})
	return out
}

// Crosswalk is one vendor code namespace at one vendor release, and its
// versions, oldest first.
type Crosswalk struct {
	ID            id.CrosswalkID
	TenantID      id.TenantID
	Vendor        string
	Namespace     string
	VendorRelease string
	Title         string
	CreatedAt     time.Time
	CreatedBy     id.UserID
	Versions      []Version
}

// Validate checks the identity.
func (c Crosswalk) Validate() error {
	if !nameShape.MatchString(c.Vendor) {
		return errs.Invalid("vendor", errs.ReasonInvalidValue, "A vendor is upper-case letters, digits, '_', '.' or '-', at most 64.")
	}
	if !nameShape.MatchString(c.Namespace) {
		return errs.Invalid("namespace", errs.ReasonInvalidValue, "A code namespace is upper-case letters, digits, '_', '.' or '-', at most 64.")
	}
	if err := text("vendorRelease", c.VendorRelease, true, 64); err != nil {
		return err
	}
	return text("title", c.Title, true, MaxText)
}

// Latest is the newest version.
func (c Crosswalk) Latest() (Version, bool) {
	if len(c.Versions) == 0 {
		return Version{}, false
	}
	return c.Versions[len(c.Versions)-1], true
}

// Current is the version in use: the newest APPROVED or SUSPENDED one. A
// SUSPENDED current version serves nothing (Lookup quarantines it), and that
// is the point of naming it — a suspension does not fall back to an older
// version.
func (c Crosswalk) Current() (Version, bool) {
	for i := len(c.Versions) - 1; i >= 0; i-- {
		if s := c.Versions[i].Status; s == StatusApproved || s == StatusSuspended {
			return c.Versions[i], true
		}
	}
	return Version{}, false
}

// Open reports whether a version is still being prepared or reviewed; a
// crosswalk has at most one such version at a time.
func (c Crosswalk) Open() (Version, bool) {
	for _, v := range c.Versions {
		if v.Status == StatusDraft || v.Status == StatusInReview {
			return v, true
		}
	}
	return Version{}, false
}

// Version returns version n.
func (c Crosswalk) Version(n int) (Version, bool) {
	for _, v := range c.Versions {
		if v.Number == n {
			return v, true
		}
	}
	return Version{}, false
}

// Version is one immutable set of mappings and the grant they are held
// under.
type Version struct {
	Number        int
	Rights        Rights
	EffectiveFrom time.Time
	// EffectiveTo is the first day the version no longer applies; zero for
	// open-ended (end-exclusive, Doc 03 WF-07 "[valid_from, valid_to)").
	EffectiveTo time.Time
	Entries     []Entry
	// Digest is the canonical digest of the entries, rights and window: what
	// an approval is an approval of.
	Digest     canonical.Digest
	IntakeKey  string
	IntakeHash canonical.Digest
	CreatedAt  time.Time
	CreatedBy  id.UserID
	Status     Status
	History    []Event
}

// Revision is the version's event count: what an act names to prove it was
// decided on the record as it now stands.
func (v Version) Revision() int { return len(v.History) }

// Validate checks the version before it is written.
func (v Version) Validate() error {
	if err := v.Rights.Validate(); err != nil {
		return err
	}
	if v.EffectiveFrom.IsZero() {
		return errs.Invalid("effectiveFrom", errs.ReasonMissingField, "A crosswalk version is effective-dated.")
	}
	if !v.EffectiveTo.IsZero() && !v.EffectiveTo.After(v.EffectiveFrom) {
		return errs.Invalid("effectiveTo", errs.ReasonInvalidValue, "effectiveTo is after effectiveFrom (the window is end-exclusive).")
	}
	return CheckEntries(v.Entries)
}

// InForceOn reports whether day lies inside the version's window.
func (v Version) InForceOn(day time.Time) bool {
	return !day.Before(v.EffectiveFrom) && (v.EffectiveTo.IsZero() || day.Before(v.EffectiveTo))
}

// Content is the canonical value a version's digest is taken over: the
// mappings, the grant and the window. The identity of the crosswalk is in it,
// so the same rows for another vendor release are a different digest.
func Content(c Crosswalk, v Version) canonical.Value {
	rows := make([]canonical.Value, 0, len(v.Entries))
	for _, e := range SortEntries(v.Entries) {
		rows = append(rows, canonical.Object(
			canonical.F("externalCode", canonical.String(e.ExternalCode)),
			canonical.F("description", canonical.String(e.Description)),
			canonical.F("match", canonical.String(string(e.Match))),
			canonical.F("serviceTarget", canonical.OptString(string(e.ServiceTarget))),
			canonical.F("levyHint", canonical.OptString(e.LevyHint)),
			canonical.F("predicate", canonical.OptString(e.Predicate)),
			canonical.F("note", canonical.OptString(e.Note)),
		))
	}
	return canonical.Object(
		canonical.F("vendor", canonical.String(c.Vendor)),
		canonical.F("namespace", canonical.String(c.Namespace)),
		canonical.F("vendorRelease", canonical.String(c.VendorRelease)),
		canonical.F("rights", canonical.Object(
			canonical.F("basis", canonical.String(string(v.Rights.Basis))),
			canonical.F("reference", canonical.String(v.Rights.Reference)),
			canonical.F("expiresOn", optDay(v.Rights.ExpiresOn)),
			canonical.F("evidenceSha256", canonical.OptString(v.Rights.EvidenceSHA256)),
		)),
		canonical.F("effectiveFrom", canonical.String(v.EffectiveFrom.Format(time.DateOnly))),
		canonical.F("effectiveTo", optDay(v.EffectiveTo)),
		canonical.F("entries", canonical.Array(rows...)),
	)
}

// Payload is what an intake request is hashed over for idempotency: the
// content, the title and whether it was submitted as it was written.
func Payload(c Crosswalk, v Version, submit bool) canonical.Value {
	return canonical.Object(
		canonical.F("title", canonical.String(c.Title)),
		canonical.F("content", Content(c, v)),
		canonical.F("submit", canonical.Bool(submit)),
	)
}

func optDay(t time.Time) canonical.Value {
	if t.IsZero() {
		return canonical.Absent()
	}
	return canonical.String(t.Format(time.DateOnly))
}

func text(field, value string, required bool, limit int) error {
	switch {
	case value == "" && required:
		return errs.Invalid(field, errs.ReasonMissingField, fmt.Sprintf("%s is required.", field))
	case len(value) > limit:
		return errs.Invalid(field, errs.ReasonInvalidValue, fmt.Sprintf("%s is at most %d characters.", field, limit))
	case value != strings.TrimSpace(value):
		return errs.Invalid(field, errs.ReasonInvalidValue, fmt.Sprintf("%s carries no leading or trailing space.", field))
	}
	return nil
}
