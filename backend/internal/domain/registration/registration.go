// Package registration models the legal-entity registration ledger and the
// exemption certificates held as evidence (Engineering Doc 17, G-048;
// Doc 08 OBL-02; Doc 15 OPS-13 and OPS-14).
//
// Doc 17's first principle is the reason this package exists apart from tax
// determination, and the reason nothing here reads or writes a decision:
//
//	A certificate is evidence, not a universal tax-exemption rule. A
//	registration is evidence of status, not proof a particular return is
//	due or a rate applies.
//
// So three things are kept apart:
//
//   - A record is not its verification. A registration number entered by a
//     person is DRAFT or SUBMITTED; it is VERIFIED only on an authority check
//     a different person records with the authority's own reference, and an
//     INCONCLUSIVE or UNAVAILABLE check never counts as one (Doc 17 §07).
//     No authority adapter is connected, so every check here is a manual
//     lookup a named person attests; none is fabricated.
//   - Verification is not approval. A VERIFIED registration is ACTIVE only
//     when someone other than its submitter activates it on its exact
//     version, and only from its effective date. A screened certificate is
//     APPROVED only when a checker other than its recommender and its
//     uploader confirms a recommendation made on its exact evidence version.
//   - Approval is not a tax treatment. An APPROVED certificate or an ACTIVE
//     registration changes no tax result: the determination engine reads its
//     own approved rules, and a later approval never rewrites a committed
//     result (Doc 17 §01, NAV-008).
//
// Nothing is edited. A record's state is the fold of its appended events:
// a replacement document is a new evidence version, a correction of an
// effective date is a new registration superseding the old, and an expired
// or revoked record stays visible as recorded.
//
// Documents are evidence by digest: the browser hashes a file with SHA-256
// and only the digest, name, type and size are recorded. The file itself is
// not uploaded, scanned or stored.
package registration

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/remittance"
)

// The Doc 17 reason codes.
var (
	// ReasonVersionConflict is an approval of a version that is no longer
	// the current one (Doc 17 ER-042, XC-08).
	ReasonVersionConflict = errs.Register("VERSION_CONFLICT", errs.CategoryConflict,
		"g-048",
		"The record changed after it was reviewed: a new document, check or event was recorded. Nothing was approved. Re-read the record and review its current version.")

	// ReasonSchemeMismatch is an identifier entered under the wrong
	// registration scheme (Doc 17 ER-011, XC-05). It is never converted.
	ReasonSchemeMismatch = errs.Register("REGISTRATION_SCHEME_MISMATCH", errs.CategoryValidation,
		"g-048",
		"The identifier has the shape of a different registration scheme. An FCC FRN, a USAC 499 Filer ID and a USAC 498 ID are distinct and never converted into each other; enter it under its own scheme.")

	// ReasonProvenanceConflict is evidence that belongs to someone else: a
	// document already held for a different buyer or seller entity, or a
	// registration number already held by another of the tenant's legal
	// entities (Doc 17 EER-08, EER-10; OBL-02).
	ReasonProvenanceConflict = errs.Register("EVIDENCE_PROVENANCE_CONFLICT", errs.CategoryConflict,
		"g-048",
		"This evidence is already recorded for a different buyer or legal entity. Evidence belonging to one buyer or entity never carries over to another.")

	// ReasonMakerChecker is the estate's maker-checker code.
	ReasonMakerChecker = remittance.ReasonMakerChecker
)

// Bounds on the free-text fields.
const (
	MaxText      = 200
	MaxReason    = 2000
	MaxFilename  = 255
	MaxDocument  = 25 << 20 // 25 MiB, the intake limit (Doc 17 EER-09)
	ExpiringDays = 60       // how far ahead "expiring soon" looks
)

var (
	sha256Shape       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	jurisdictionShape = regexp.MustCompile(`^[A-Z]{2}(-[A-Z0-9]{1,3})?$`)
)

// AllowedMediaTypes are the document types intake accepts: PDF and images
// (Doc 17 EER-09).
var AllowedMediaTypes = []string{"application/pdf", "image/png", "image/jpeg", "image/tiff", "image/webp"}

// Attachment is a document recorded as evidence by its SHA-256. Nothing is
// uploaded; the digest is what a later copy of the file is checked against.
type Attachment struct {
	SHA256    string
	Filename  string
	MediaType string
	Bytes     int64
}

// Normalize trims and lowercases what is compared.
func (a Attachment) Normalize() Attachment {
	a.SHA256 = strings.ToLower(strings.TrimSpace(a.SHA256))
	a.Filename = strings.TrimSpace(a.Filename)
	a.MediaType = strings.ToLower(strings.TrimSpace(a.MediaType))
	return a
}

// Validate refuses an attachment intake would not accept: a malformed
// digest, a type that is not a PDF or an image, or an empty or oversized
// file. Refused before anything is recorded (Doc 17 ER-026).
func (a Attachment) Validate() error {
	switch {
	case !sha256Shape.MatchString(a.SHA256):
		return errs.Invalid("sha256", errs.ReasonInvalidValue, "The digest is the document's SHA-256 in lowercase hex.")
	case a.Filename == "" || len(a.Filename) > MaxFilename:
		return errs.Invalid("filename", errs.ReasonInvalidValue, fmt.Sprintf("A document has a file name of 1 to %d characters.", MaxFilename))
	case !allowedMedia(a.MediaType):
		return errs.Invalid("mediaType", errs.ReasonInvalidValue,
			"Only a PDF or an image (PNG, JPEG, TIFF, WebP) is accepted as evidence.")
	case a.Bytes <= 0 || a.Bytes > MaxDocument:
		return errs.Invalid("bytes", errs.ReasonInvalidValue, "A document is between 1 byte and 25 MiB.")
	}
	return nil
}

func allowedMedia(t string) bool {
	for _, m := range AllowedMediaTypes {
		if m == t {
			return true
		}
	}
	return false
}

// ValidJurisdiction reports whether j is a country, or a country and one
// subdivision: US, US-NY, GB, CA-ON.
func ValidJurisdiction(j string) bool { return jurisdictionShape.MatchString(j) }

// Day is the civil date of an instant, in UTC, as a midnight. Doc 17 asks
// for the governing authority's local date; until the content packs carry
// an authority calendar, every boundary here is a UTC date, and the screens
// say so.
func Day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Flag is something about a record a person should look at, as of a date.
type Flag string

// The flags.
const (
	FlagExpiringSoon         Flag = "EXPIRING_SOON"
	FlagPastExpiry           Flag = "PAST_EXPIRY"
	FlagNotYetEffective      Flag = "NOT_YET_EFFECTIVE"
	FlagPendingValidation    Flag = "PENDING_VALIDATION"
	FlagAuthorityNegative    Flag = "AUTHORITY_NEGATIVE"
	FlagAwaitingActivation   Flag = "AWAITING_ACTIVATION"
	FlagCorrectionRequested  Flag = "CORRECTION_REQUESTED"
	FlagAwaitingSecondReview Flag = "AWAITING_SECOND_REVIEW"
)

// Valid reports whether the flag is one of these.
func (f Flag) Valid() bool {
	switch f {
	case FlagExpiringSoon, FlagPastExpiry, FlagNotYetEffective, FlagPendingValidation, FlagAuthorityNegative,
		FlagAwaitingActivation, FlagCorrectionRequested, FlagAwaitingSecondReview:
		return true
	}
	return false
}

// endFlags are the date flags of a record whose end date is end (zero for
// none) and which is in force-capable state live.
func endFlags(live bool, start, end, today time.Time) []Flag {
	if !live {
		return nil
	}
	var out []Flag
	if !start.IsZero() && start.After(today) {
		out = append(out, FlagNotYetEffective)
	}
	if !end.IsZero() {
		switch {
		case today.After(end):
			out = append(out, FlagPastExpiry)
		case !end.After(today.AddDate(0, 0, ExpiringDays)):
			out = append(out, FlagExpiringSoon)
		}
	}
	return out
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

func reasonText(field, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errs.Invalid(field, errs.ReasonMissingField, "This act gives its reason.")
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
