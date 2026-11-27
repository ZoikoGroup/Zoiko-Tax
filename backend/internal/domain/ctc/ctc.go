// Package ctc is the e-invoicing and continuous transaction controls layer
// (Engineering Doc 21, G-030): which of a legal entity's fiscal documents owe
// an electronic document duty in which country, whether a document meets the
// applicable format profile's rules, and what happened when it was submitted
// to the authority.
//
// It sits on top of the fiscal documents of ZTAX-FIN-001 and duplicates
// nothing below it. A document's tax, totals and lineage are the committed
// fiscal document's; nothing here recomputes a rate, edits an amount or
// invents a correction (Doc 21 §01 "Source of fiscal truth"). Three rules
// shape the package:
//
//   - Applicability first, and per cell. A country duty is a record for one
//     legal entity, one country, one document type and one kind of buyer,
//     with its legal source and effective dates. It becomes ACTIVE only on
//     the G0–G4 sign-offs Doc 21 §11 demands, each by someone other than its
//     author, with evidence attached, and by an activator who is neither.
//     No country is activated because a format or adapter exists; absence of
//     an active record is REVIEW_BLOCKED, never a guess (Doc 21 W01, A02,
//     A03, A58, A59; Sidebar Architecture §10).
//   - Validation is deterministic and versioned. The same document under the
//     same record version and ruleset gives the same rule results, stored
//     immutably with the ruleset version. Structural pre-checks pass or
//     fail; the official schema/Schematron stage is not run here and says so
//     (NOT_EVALUATED), because no licensed validation bundle is installed
//     (Doc 21 W06, A16–A18).
//   - Transport is not acceptance. A submission is sealed when prepared,
//     dispatched once by someone other than its preparer, and moves only on
//     the authority's answers as a person recorded them with evidence. A
//     transport receipt leaves the authority outcome pending; an uncertain
//     outcome is never retried without a status enquiry; a final outcome is
//     never overwritten (Doc 21 W14, W15, A40–A45). No authority or partner
//     connector is wired here.
package ctc

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/registration"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/remittance"
)

// The Doc 21 reason codes.
var (
	// ReasonNoApplicability is a cell — entity, country, document type,
	// buyer kind, date — no applicability record covers (Doc 21 W01).
	ReasonNoApplicability = errs.Register("CTC_NO_APPLICABILITY", errs.CategoryUnsupported,
		"g-030",
		"No country applicability record covers this legal entity, country, document type and buyer kind on this date. The duty is REVIEW_BLOCKED until one is recorded, signed off and activated; nothing is assumed.")

	// ReasonApplicabilityNotActive is a record that is not ACTIVE: in draft,
	// in review, suspended or retired. Nothing is validated for dispatch or
	// submitted under it.
	ReasonApplicabilityNotActive = errs.Register("CTC_APPLICABILITY_NOT_ACTIVE", errs.CategoryConflict,
		"g-030",
		"The applicability record is not ACTIVE. Nothing is submitted under a draft, in-review, suspended or retired record.")

	// ReasonApplicabilityConflict is two records claiming the same cell for
	// overlapping dates (Doc 21 A03).
	ReasonApplicabilityConflict = errs.Register("CTC_APPLICABILITY_CONFLICT", errs.CategoryConflict,
		"g-030",
		"Another active or suspended applicability record covers the same legal entity, country, document type and buyer kind for overlapping dates. Retire or narrow it first; conflicting records are never both in force.")

	// ReasonGateMissing is an activation without every required G0–G4
	// sign-off on the current version (Doc 21 §11, A41, A59).
	ReasonGateMissing = errs.Register("CTC_GATE_SIGNOFF_MISSING", errs.CategoryConflict,
		"g-030",
		"A required G0–G4 release gate has no sign-off on the record's current version. The record stays NOT_ACTIVATED until every required gate is signed.")

	// ReasonEvidenceMissing is an activation with no evidence attached.
	ReasonEvidenceMissing = errs.Register("CTC_EVIDENCE_MISSING", errs.CategoryConflict,
		"g-030",
		"The applicability record has no evidence document attached. A record is activated only on recorded evidence of its legal source.")

	// ReasonProfileCountryMismatch is a national format profile named for
	// another country.
	ReasonProfileCountryMismatch = errs.Register("CTC_PROFILE_COUNTRY_MISMATCH", errs.CategoryValidation,
		"g-030",
		"This format profile belongs to another country's regime. A national profile is never applied outside its own country.")

	// ReasonDocumentMismatch is a document that does not belong to the
	// record's cell: another legal entity, or another document type (Doc 21
	// A06).
	ReasonDocumentMismatch = errs.Register("CTC_DOCUMENT_MISMATCH", errs.CategoryConflict,
		"g-030",
		"The fiscal document belongs to a different legal entity or is a different document type from the applicability record. One entity's record never carries another's documents.")

	// ReasonSubmissionProhibited is a submission under a PROHIBITED duty.
	ReasonSubmissionProhibited = errs.Register("CTC_SUBMISSION_PROHIBITED", errs.CategoryConflict,
		"g-030",
		"The applicability record says this electronic document is PROHIBITED for this cell. Nothing is submitted.")

	// ReasonValidationRequired is a submission without a passing validation
	// of the document under the record's current version and ruleset.
	ReasonValidationRequired = errs.Register("CTC_VALIDATION_REQUIRED", errs.CategoryConflict,
		"g-030",
		"The document has no passing format validation under the record's current version and the current ruleset. Validate it first; a failed validation blocks submission.")

	// ReasonValidationStale is a validation, or a sealed submission, made
	// against inputs that have since changed: the document, the record's
	// version or the ruleset (Doc 21 A18).
	ReasonValidationStale = errs.Register("CTC_VALIDATION_STALE", errs.CategoryConflict,
		"g-030",
		"The validation or the sealed payload no longer matches the document, the applicability record's version or the current ruleset. A previously green result is not reused; validate and prepare again.")

	// ReasonPayloadDigestMismatch is a dispatch naming a digest other than the
	// sealed payload's.
	ReasonPayloadDigestMismatch = errs.Register("CTC_PAYLOAD_DIGEST_MISMATCH", errs.CategoryConflict,
		"g-030",
		"The dispatch named a payload digest other than the submission's sealed one. Nothing was dispatched; review the submission as it is.")

	// ReasonOutcomeUncertain is a dispatch while the authority's outcome of
	// an earlier attempt is unknown (Doc 21 W14, A42).
	ReasonOutcomeUncertain = errs.Register("CTC_OUTCOME_UNCERTAIN", errs.CategoryConflict,
		"g-030",
		"The authority's outcome of an earlier attempt is unknown. Record a status enquiry first; nothing is sent again until the enquiry found nothing under the submission's reference.")

	// ReasonFinalOutcome is an answer recorded against a submission whose
	// authority outcome is already final (Doc 21 W15, A27, A45).
	ReasonFinalOutcome = errs.Register("CTC_FINAL_OUTCOME_RECORDED", errs.CategoryConflict,
		"g-030",
		"The authority's final outcome is already recorded for this submission. A later or out-of-order answer never overwrites it; a correction is a new document.")

	// ReasonVersionConflict is the estate's version-conflict code.
	ReasonVersionConflict = registration.ReasonVersionConflict

	// ReasonMakerChecker is the estate's maker-checker code.
	ReasonMakerChecker = remittance.ReasonMakerChecker
)

// Bounds on free text.
const (
	MaxText   = 200
	MaxReason = 2000
	MaxRef    = 200
)

var (
	countryShape = regexp.MustCompile(`^[A-Z]{2}$`)
	sha256Shape  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	refShape     = regexp.MustCompile(`^[\x21-\x7E][\x20-\x7E]{0,199}$`)
)

// Attachment is an evidence document recorded by its browser-computed
// SHA-256. Nothing is uploaded.
type Attachment = registration.Attachment

// ValidCountry reports whether c is an ISO 3166-1 alpha-2 code.
func ValidCountry(c string) bool { return countryShape.MatchString(c) }

// ValidSHA256 reports whether s is a lowercase hex SHA-256.
func ValidSHA256(s string) bool { return sha256Shape.MatchString(s) }

// Day is the civil date of an instant, in UTC, as a midnight.
func Day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// BuyerKind is who receives the document: a business, a government body or a
// consumer. A B2C document is never forced through a B2B rail because of an
// address (Doc 21 A30).
type BuyerKind string

// The buyer kinds.
const (
	BuyerB2B BuyerKind = "B2B"
	BuyerB2G BuyerKind = "B2G"
	BuyerB2C BuyerKind = "B2C"
)

// BuyerKinds is every buyer kind.
var BuyerKinds = []BuyerKind{BuyerB2B, BuyerB2G, BuyerB2C}

// Valid reports whether the kind is one of these.
func (k BuyerKind) Valid() bool { return slices.Contains(BuyerKinds, k) }

// Duty is what an applicability record says about the electronic document
// (Doc 21 §01 "Applicability first"). REVIEW_BLOCKED is not a duty a record
// holds: it is what resolution answers when no active record decides.
type Duty string

// The duties.
const (
	DutyRequired      Duty = "REQUIRED"
	DutyOptional      Duty = "OPTIONAL"
	DutyProhibited    Duty = "PROHIBITED"
	DutyReviewBlocked Duty = "REVIEW_BLOCKED"
)

// Valid reports whether d is a duty a record may hold.
func (d Duty) Valid() bool { return d == DutyRequired || d == DutyOptional || d == DutyProhibited }

// Dispatches reports whether a document under this duty may be submitted.
func (d Duty) Dispatches() bool { return d == DutyRequired || d == DutyOptional }

// Model is the control model of the duty (Doc 21 §02 "clearance_model").
type Model string

// The models.
const (
	// ModelClearance: the authority clears the document before it is
	// legally issued (IT SdI, BR NFCom, SA standard invoices, IN IRP).
	ModelClearance Model = "CLEARANCE"
	// ModelRealTimeReporting: the document is reported to the authority as
	// or after it is issued (FR e-reporting, SA simplified invoices).
	ModelRealTimeReporting Model = "REAL_TIME_REPORTING"
	// ModelPostAudit: the document is exchanged and kept for audit, with no
	// transaction-time authority step.
	ModelPostAudit Model = "POST_AUDIT"
	// ModelNetworkExchange: the document is exchanged over an
	// interoperability network such as Peppol.
	ModelNetworkExchange Model = "NETWORK_EXCHANGE"
	// ModelNone: no electronic document duty. Only a PROHIBITED record holds
	// it.
	ModelNone Model = "NONE"
)

// Models is every model.
var Models = []Model{ModelClearance, ModelRealTimeReporting, ModelPostAudit, ModelNetworkExchange, ModelNone}

// Valid reports whether the model is one of these.
func (m Model) Valid() bool { return slices.Contains(Models, m) }

// Profile is a format profile: the semantic model and national
// specialisation a document is projected into (Doc 21 §08).
type Profile string

// The profiles of Doc 21 §08's adapter list.
const (
	ProfilePeppolBIS3  Profile = "PEPPOL_BIS_3"
	ProfileFREN16931   Profile = "FR_EN16931"
	ProfileITFatturaPA Profile = "IT_FATTURAPA"
	ProfilePLKSeFFA    Profile = "PL_KSEF_FA"
	ProfileBRNFCom62   Profile = "BR_NFCOM_62"
	ProfileSAZATCAUBL  Profile = "SA_ZATCA_UBL"
	ProfileINGSTIRP    Profile = "IN_GST_IRP"
	ProfileNone        Profile = "NONE"
)

// profileCountry is the one country a national profile belongs to. A profile
// absent here is not national: Peppol BIS is a network profile used across
// countries.
var profileCountry = map[Profile]string{
	ProfileFREN16931: "FR", ProfileITFatturaPA: "IT", ProfilePLKSeFFA: "PL", ProfileBRNFCom62: "BR",
	ProfileSAZATCAUBL: "SA", ProfileINGSTIRP: "IN",
}

// Profiles is every profile.
var Profiles = []Profile{ProfilePeppolBIS3, ProfileFREN16931, ProfileITFatturaPA, ProfilePLKSeFFA, ProfileBRNFCom62,
	ProfileSAZATCAUBL, ProfileINGSTIRP, ProfileNone}

// Valid reports whether the profile is one of these.
func (p Profile) Valid() bool { return slices.Contains(Profiles, p) }

// Country is the country a national profile belongs to, or empty.
func (p Profile) Country() string { return profileCountry[p] }

// DocumentTypes are the fiscal document types an applicability record may
// name: the billing documents and their corrections that a regime projects
// into an electronic document. A VOID, REFUND, AMENDMENT or RESTATEMENT has no
// projection here; a regime's cancellation flow is outside this slice.
var DocumentTypes = []document.Type{document.TypeInvoice, document.TypeCreditNote, document.TypeDebitNote,
	document.TypeRebill, document.TypeAdjustment}

// ValidDocumentType reports whether t is one of DocumentTypes.
func ValidDocumentType(t document.Type) bool { return slices.Contains(DocumentTypes, t) }

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

func ref(field, value string, required bool) error {
	if value == "" {
		if required {
			return errs.Invalid(field, errs.ReasonMissingField, fmt.Sprintf("%s is required.", field))
		}
		return nil
	}
	if !refShape.MatchString(value) || value != strings.TrimSpace(value) {
		return errs.Invalid(field, errs.ReasonInvalidValue, fmt.Sprintf("%s is 1 to %d printable characters.", field, MaxRef))
	}
	return nil
}

func reasonText(field, reason string, required bool) error {
	if strings.TrimSpace(reason) == "" {
		if required {
			return errs.Invalid(field, errs.ReasonMissingField, "This act gives its reason.")
		}
		return nil
	}
	if len(reason) > MaxReason {
		return errs.Invalid(field, errs.ReasonInvalidValue, fmt.Sprintf("A reason is at most %d characters.", MaxReason))
	}
	return nil
}

func invalidMove(what, from, to string) error {
	return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
		fmt.Sprintf("A %s does not move from %s to %s.", what, from, to))
}

func makerChecker(detail string) error {
	return errs.New(errs.CategoryPolicy, ReasonMakerChecker, detail)
}
