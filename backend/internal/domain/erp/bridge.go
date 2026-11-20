// Package erp is the ERP journal bridge and the subledger-to-GL
// reconciliation (LED-23 ERP Journal Bridge, Export & Acknowledgment; LED-18's
// "compare GL balance"; ZTAX-FIN-001 §13 and R6 of §17).
//
// The Tax Control Subledger is not the customer's general ledger
// (ZTAX-FIN-REQ-0033, -0034). What leaves it for the ERP is an export batch:
// an exact, sealed set of posted TCSL journals mapped onto the customer's
// chart of accounts by one versioned mapping. Four things are kept apart here
// because confusing them is how a GL ends up with a tax journal twice, or
// with none and nobody noticing:
//
//   - A batch is sealed when it is prepared. Its digest covers the journal
//     IDs, every mapped line and the mapping version, so what a checker
//     authorises is exactly what is dispatched (QA-LED-067, -068).
//   - Transport is not posting. TRANSMITTED says the dispatch was committed to
//     the transactional outbox; ACKNOWLEDGED says the ERP's endpoint received
//     it; only ERP_POSTED, carrying the ERP's own posted journal reference,
//     says the general ledger holds it. A reconciliation counts only that.
//   - A retry is the same batch. Every attempt carries the batch's one
//     external reference and digest, so an ERP that deduplicates on it posts
//     one GL journal however many times the batch is sent (QA-LED-069). An
//     outcome nobody knows is never answered by sending again blind: a status
//     query that found nothing must be recorded first.
//   - Nothing here pretends to be the ERP. No connector is wired to a real
//     ledger; every ERP answer — receipt, posting, rejection, unknown outcome,
//     status query — is recorded by a person through the API with its
//     evidence, and appended, never edited.
//
// Every amount is a fiscal.Money, added and compared exactly.
package erp

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/remittance"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// The package's reason codes.
var (
	// ReasonMappingIncomplete is a journal line on a TCSL account the mapping
	// does not map. The export is held rather than routed to a generic
	// suspense account (LED-03 "Missing mapping"; ZTAX-FIN-REQ-0054).
	ReasonMappingIncomplete = errs.Register("ERP_MAPPING_INCOMPLETE", errs.CategoryConflict,
		"led-23",
		"The ERP account mapping has no GL account for a TCSL account this export needs. Nothing was exported; record a mapping version that maps it.")

	// ReasonMappingChanged is a dispatch of a batch sealed under a mapping
	// version that is no longer the bridge's current one (QA-LED-068).
	ReasonMappingChanged = errs.Register("ERP_MAPPING_CHANGED", errs.CategoryConflict,
		"led-23",
		"The bridge's account mapping changed after this batch was prepared. Withdraw it and prepare a new batch under the current mapping.")

	// ReasonBatchDigestMismatch is a dispatch naming a digest that is not the
	// batch's: the checker reviewed something other than what would be sent.
	ReasonBatchDigestMismatch = errs.Register("ERP_BATCH_DIGEST_MISMATCH", errs.CategoryConflict,
		"led-23",
		"The digest given is not this batch's sealed digest. Review the batch as it is and authorise that.")

	// ReasonOutcomeUnknown is a re-dispatch of a batch whose last outcome is
	// unknown, with no status query recorded since (LED-23 "External status
	// unknown").
	ReasonOutcomeUnknown = errs.Register("ERP_OUTCOME_UNKNOWN", errs.CategoryConflict,
		"led-23",
		"The ERP's outcome for this batch is unknown. Query the ERP's status by the batch's external reference and record the answer before sending it again.")

	// ReasonNothingToExport is a prepare that found no posted journal not
	// already in a live batch.
	ReasonNothingToExport = errs.Register("ERP_NOTHING_TO_EXPORT", errs.CategoryConflict,
		"led-23",
		"Every posted journal of this legal entity, period and currency is already in a live export batch.")
)

// ReasonMakerChecker is the estate's maker-checker code: the person who
// prepared a batch does not also authorise its dispatch.
var ReasonMakerChecker = remittance.ReasonMakerChecker

// Vendor is the ERP family a bridge targets. The bridge is vendor-agnostic:
// the vendor is a label for the connector that consumes the dispatch, and
// each real target still needs its own contract verification (LED-23).
type Vendor string

// The vendors.
const (
	VendorSAP      Vendor = "SAP"
	VendorOracle   Vendor = "ORACLE"
	VendorNetSuite Vendor = "NETSUITE"
	VendorDynamics Vendor = "DYNAMICS"
	VendorOther    Vendor = "OTHER"
)

// Valid reports whether the vendor is one this service knows.
func (v Vendor) Valid() bool {
	switch v {
	case VendorSAP, VendorOracle, VendorNetSuite, VendorDynamics, VendorOther:
		return true
	}
	return false
}

// Bounds on the free-text fields.
const (
	MaxName   = 200
	MaxRef    = 200
	MaxDetail = 2000
)

var (
	companyCodeShape = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)
	glAccountShape   = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,60}$`)
	periodShape      = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)
	currencyShape    = regexp.MustCompile(`^[A-Z]{3}$`)
	digestRefShape   = regexp.MustCompile(`^[A-Za-z0-9:_+/=.-]{1,200}$`)
)

// ValidPeriod reports whether p is a legal period, YYYY-MM.
func ValidPeriod(p string) bool { return periodShape.MatchString(p) }

// ValidCurrency reports whether c is an ISO 4217 code's shape.
func ValidCurrency(c string) bool { return currencyShape.MatchString(c) }

// ValidGLAccount reports whether a is a customer GL account code's shape.
func ValidGLAccount(a string) bool { return glAccountShape.MatchString(a) }

func text(field, value string, required bool, max int) error {
	switch {
	case value == "" && required:
		return errs.Invalid(field, errs.ReasonMissingField, fmt.Sprintf("%s is required.", field))
	case len(value) > max:
		return errs.Invalid(field, errs.ReasonInvalidValue, fmt.Sprintf("%s is at most %d characters.", field, max))
	case value != strings.TrimSpace(value):
		return errs.Invalid(field, errs.ReasonInvalidValue, fmt.Sprintf("%s carries no leading or trailing space.", field))
	}
	return nil
}

// Bridge is one ERP journal bridge: a member legal entity's subledger
// connected to one customer general ledger, under a company code, with its
// account mapping versions oldest first.
type Bridge struct {
	ID          id.ERPBridgeID
	TenantID    id.TenantID
	LegalEntity id.LegalEntityID
	Name        string
	Vendor      Vendor
	// CompanyCode is the entity's company code in the ERP.
	CompanyCode string
	CreatedAt   time.Time
	CreatedBy   id.UserID
	Mappings    []Mapping
}

// Validate refuses a bridge that cannot be recorded.
func (b Bridge) Validate() error {
	if err := text("name", b.Name, true, MaxName); err != nil {
		return err
	}
	if !b.Vendor.Valid() {
		return errs.Invalid("vendor", errs.ReasonInvalidValue, fmt.Sprintf("%q is not an ERP vendor this service knows.", b.Vendor))
	}
	if !companyCodeShape.MatchString(b.CompanyCode) {
		return errs.Invalid("companyCode", errs.ReasonInvalidValue,
			"A company code is 1 to 40 letters, digits, dots, underscores or hyphens.")
	}
	if b.LegalEntity.IsZero() {
		return errs.Invalid("legalEntityId", errs.ReasonMissingField, "A bridge belongs to a member legal entity.")
	}
	return nil
}

// Current returns the bridge's newest mapping version, and false when it has
// none yet.
func (b Bridge) Current() (Mapping, bool) {
	if len(b.Mappings) == 0 {
		return Mapping{}, false
	}
	return b.Mappings[len(b.Mappings)-1], true
}

// AccountMap is one mapped TCSL account.
type AccountMap struct {
	Account   subledger.Account
	GLAccount string
}

// Mapping is one version of a bridge's account mapping: each TCSL control
// account the bridge exports, onto one of the customer's GL accounts, under
// a named chart-of-accounts version (ZTAX-FIN-REQ-0051). A version is never
// edited; a change is the next version.
type Mapping struct {
	Version      int
	ChartVersion string
	// Accounts is sorted by TCSL account.
	Accounts   []AccountMap
	Digest     canonical.Digest
	RecordedAt time.Time
	RecordedBy id.UserID
}

// GLAccountFor returns the GL account a TCSL account maps to.
func (m Mapping) GLAccountFor(a subledger.Account) (string, bool) {
	for _, am := range m.Accounts {
		if am.Account == a {
			return am.GLAccount, true
		}
	}
	return "", false
}

// Normalise sorts the accounts, so a mapping's digest does not depend on the
// order it was sent in.
func (m Mapping) Normalise() Mapping {
	m.Accounts = slices.Clone(m.Accounts)
	slices.SortFunc(m.Accounts, func(a, b AccountMap) int { return strings.Compare(string(a.Account), string(b.Account)) })
	return m
}

// Validate refuses a mapping that cannot be recorded: every account a TCSL
// control account, each mapped once, onto a GL account code.
func (m Mapping) Validate() error {
	if m.Version < 1 {
		return errs.Invalid("version", errs.ReasonInvalidValue, "A mapping version is 1 or more.")
	}
	if err := text("chartVersion", m.ChartVersion, true, MaxRef); err != nil {
		return err
	}
	if len(m.Accounts) == 0 {
		return errs.Invalid("accounts", errs.ReasonMissingField, "A mapping maps at least one TCSL account.")
	}
	seen := map[subledger.Account]bool{}
	for _, am := range m.Accounts {
		if !am.Account.Canonical() {
			return errs.Invalid("accounts.account", errs.ReasonInvalidValue,
				fmt.Sprintf("%q is not a TCSL control account. A mapping maps the canonical accounts onto the customer's chart, never the reverse.", am.Account))
		}
		if seen[am.Account] {
			return errs.Invalid("accounts.account", errs.ReasonInvalidValue, fmt.Sprintf("%s is mapped twice.", am.Account))
		}
		seen[am.Account] = true
		if !glAccountShape.MatchString(am.GLAccount) {
			return errs.Invalid("accounts.glAccount", errs.ReasonInvalidValue,
				"A GL account is 1 to 60 letters, digits, dots, underscores, colons, slashes or hyphens.")
		}
	}
	return nil
}

// Seal computes the mapping's digest over the bridge it belongs to, its
// version, chart version and sorted accounts.
func (m Mapping) Seal(bridge id.ERPBridgeID) (Mapping, error) {
	m = m.Normalise()
	items := make([]canonical.Value, len(m.Accounts))
	for i, am := range m.Accounts {
		items[i] = canonical.Object(
			canonical.F("account", canonical.String(string(am.Account))),
			canonical.F("glAccount", canonical.String(am.GLAccount)),
		)
	}
	d, err := canonical.Sum(canonical.Object(
		canonical.F("artifact", canonical.String("erp-account-mapping")),
		canonical.F("bridgeId", canonical.String(bridge.String())),
		canonical.F("version", canonical.Integer(int64(m.Version))),
		canonical.F("chartVersion", canonical.String(m.ChartVersion)),
		canonical.F("accounts", canonical.Array(items...)),
	))
	if err != nil {
		return Mapping{}, err
	}
	m.Digest = d
	return m, nil
}

// ValidDigestRef reports whether s is a digest or checksum reference's shape.
func ValidDigestRef(s string) bool { return digestRefShape.MatchString(s) }
