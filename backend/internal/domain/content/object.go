package content

import (
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/sourcing"
)

// Window is a half-open validity interval [From, Until). A zero Until is open
// ended. Half-open so that a rate ending at midnight and its successor starting
// at midnight neither overlap nor leave a gap — the boundary instant belongs to
// exactly one of them, and which one is not a judgement call.
type Window struct {
	From, Until time.Time
}

// Covers reports whether at falls inside the window.
func (w Window) Covers(at time.Time) bool {
	if at.Before(w.From) {
		return false
	}
	return w.Until.IsZero() || at.Before(w.Until)
}

// Validate checks the window has a start and does not end before it.
func (w Window) Validate() error {
	if w.From.IsZero() {
		return errorf("a validity window has no start")
	}
	if !w.Until.IsZero() && !w.Until.After(w.From) {
		return errorf("a validity window ends before it starts")
	}
	return nil
}

// AuthorityClass is CONT-001 §4's source authority hierarchy. It is the
// authority axis; sourcing.SourceClass is the rights axis, and the two are
// evaluated independently (ZTAX-SRC-REQ-0006).
type AuthorityClass string

// The authority classes, highest first.
const (
	AuthorityBindingPrimary          AuthorityClass = "A0"
	AuthorityOfficialOperational     AuthorityClass = "A1"
	AuthorityOfficialExplanatory     AuthorityClass = "A2"
	AuthorityLicensedSpecialist      AuthorityClass = "B1"
	AuthorityInstitutionalSpecialist AuthorityClass = "B2"
	AuthorityUnverifiedSecondary     AuthorityClass = "C1"
	AuthorityUnknown                 AuthorityClass = "U"
)

var authorityRank = map[AuthorityClass]int{
	AuthorityBindingPrimary: 0, AuthorityOfficialOperational: 1, AuthorityOfficialExplanatory: 2,
	AuthorityLicensedSpecialist: 3, AuthorityInstitutionalSpecialist: 4, AuthorityUnverifiedSecondary: 5,
	AuthorityUnknown: 6,
}

// Valid reports whether c is one of the seven classes.
func (c AuthorityClass) Valid() bool { _, ok := authorityRank[c]; return ok }

// Outranks reports whether c is strictly higher authority than other. It is
// what the authority collision rule (CONT-001 §4, ZTAX-CONT-REQ-0003) consults
// before a lower-authority source may be read as contradicting a higher one —
// it may not, silently.
func (c AuthorityClass) Outranks(other AuthorityClass) bool {
	a, okA := authorityRank[c]
	b, okB := authorityRank[other]
	return okA && okB && a < b
}

// MayDirectlyGround reports whether a source of this class may by itself
// ground a production rule (CONT-001 §4's "production rule" column): A0 and
// A1 may; everything below supports, corroborates or discovers; U fails closed
// (ZTAX-CONT-REQ-0002).
func (c AuthorityClass) MayDirectlyGround() bool {
	return c == AuthorityBindingPrimary || c == AuthorityOfficialOperational
}

// InstrumentKind is what an instrument is (CONT-001 §3 Instrument).
type InstrumentKind string

// The instrument kinds.
const (
	InstrumentStatute           InstrumentKind = "STATUTE"
	InstrumentRegulation        InstrumentKind = "REGULATION"
	InstrumentOrder             InstrumentKind = "ORDER"
	InstrumentRuling            InstrumentKind = "RULING"
	InstrumentTariff            InstrumentKind = "TARIFF"
	InstrumentRateTable         InstrumentKind = "RATE_TABLE"
	InstrumentLicenceCondition  InstrumentKind = "LICENCE_CONDITION"
	InstrumentFilingSpec        InstrumentKind = "FILING_SPECIFICATION"
	InstrumentGuidance          InstrumentKind = "GUIDANCE"
	InstrumentForm              InstrumentKind = "FORM"
	InstrumentTechnicalStandard InstrumentKind = "TECHNICAL_SPECIFICATION"
)

// Valid reports whether k is a known kind.
func (k InstrumentKind) Valid() bool {
	switch k {
	case InstrumentStatute, InstrumentRegulation, InstrumentOrder, InstrumentRuling, InstrumentTariff,
		InstrumentRateTable, InstrumentLicenceCondition, InstrumentFilingSpec, InstrumentGuidance,
		InstrumentForm, InstrumentTechnicalStandard:
		return true
	}
	return false
}

// AuthorityInstrument is a statute, regulation, order, ruling, rate table,
// guidance, form or specification — the legal thing itself, as opposed to any
// copy of it (CONT-001 §3 "Instrument").
type AuthorityInstrument struct {
	ID          string
	AuthorityID string
	Kind        InstrumentKind
	Class       AuthorityClass
	Title       string
	// Jurisdictions are canonical ZoikoTax jurisdiction identifiers, never a
	// vendor's (ZTAX-CONT-REQ-0033).
	Jurisdictions []string
	// LegalValidity is when the instrument legally applies.
	LegalValidity Window
	PublishedAt   time.Time
}

// Validate checks the instrument is identified and placed in the hierarchy.
func (i AuthorityInstrument) Validate() error {
	switch {
	case i.ID == "" || i.AuthorityID == "":
		return errorf("an authority instrument names itself and its authority")
	case !i.Kind.Valid():
		return errorf("instrument %s has kind %q", i.ID, i.Kind)
	case !i.Class.Valid():
		return errorf("instrument %s has authority class %q; CONT-001 §4 defines A0–C1 and U", i.ID, i.Class)
	}
	return i.LegalValidity.Validate()
}

// ExtractionStatus is how far a captured source has been processed (CONT-001
// §5 extraction_status).
type ExtractionStatus string

// The extraction states.
const (
	ExtractionUnprocessed ExtractionStatus = "UNPROCESSED"
	ExtractionExtracted   ExtractionStatus = "EXTRACTED"
	ExtractionReviewed    ExtractionStatus = "REVIEWED"
)

// SourceArtifact is one immutable captured copy of a source, with its
// provenance (CONT-001 §3 "SourceDocument", §5's field table).
//
// It carries three identities that must not be confused: the instrument it is
// a copy of, the source in the register whose licence governs it (SourceID,
// whose SourceLicenseRecord is sourcing's), and its own content hash. A URL is
// not among them — a URL is where the artifact came from, not what it is, and
// "no content object may rely only on a mutable external URL for historical
// proof" (ZTAX-CONT-REQ-0076).
type SourceArtifact struct {
	ID             string
	SourceID       sourcing.SourceID
	InstrumentID   string
	AuthorityID    string
	AuthorityClass AuthorityClass
	SourceType     InstrumentKind
	Jurisdictions  []string
	PublishedAt    time.Time
	LegalEffective Window
	// RetrievedAt is knowledge time: when ZoikoTax acquired it. It is distinct
	// from PublishedAt and LegalEffective on purpose (CONT-001 §9 forbids
	// collapsing them).
	RetrievedAt time.Time
	Language    string // BCP 47
	SourceURI   string
	ContentHash string // ZTAX-CONT-REQ-0052
	LicenceRef  string
	ArchiveRef  string
	// DerivedFrom is the artifact this one was produced from by translation,
	// OCR, extraction or normalisation. Transformations produce children, never
	// replacements (CONT-001 §5, ZTAX-CONT-REQ-0053).
	DerivedFrom string
	Supersedes  string
	Extraction  ExtractionStatus
}

// Validate checks the artifact has an immutable identity and its provenance.
func (a SourceArtifact) Validate() error {
	var missing []string
	need := func(ok bool, f string) {
		if !ok {
			missing = append(missing, f)
		}
	}
	need(a.ID != "", "id")
	need(a.SourceID != "", "sourceId")
	need(a.AuthorityID != "", "authorityId")
	need(!a.RetrievedAt.IsZero(), "retrievedAt")
	need(a.Language != "", "language")
	need(a.ContentHash != "", "contentHash")
	need(a.ArchiveRef != "" || a.ContentHash != "", "archiveRef")
	if len(missing) > 0 {
		return errorf("source artifact %s is missing %s", a.ID, strings.Join(missing, ", "))
	}
	if !a.AuthorityClass.Valid() {
		return errorf("source artifact %s has authority class %q", a.ID, a.AuthorityClass)
	}
	if a.DerivedFrom == a.ID {
		return errorf("source artifact %s is derived from itself", a.ID)
	}
	switch a.Extraction {
	case ExtractionUnprocessed, ExtractionExtracted, ExtractionReviewed:
	default:
		return errorf("source artifact %s has extraction status %q", a.ID, a.Extraction)
	}
	return nil
}

// InterpretationStatus is where an interpretation stands.
type InterpretationStatus string

// The interpretation states.
const (
	InterpretationDraft      InterpretationStatus = "DRAFT"
	InterpretationInReview   InterpretationStatus = "IN_REVIEW"
	InterpretationApproved   InterpretationStatus = "APPROVED"
	InterpretationRejected   InterpretationStatus = "REJECTED"
	InterpretationSuperseded InterpretationStatus = "SUPERSEDED"
)

// Interpretation is a qualified human's statement of what the sources mean
// and what executable treatment follows (CONT-001 §3 "InterpretationRecord",
// §8's element table).
//
// It is the one object in the chain that is a human judgement rather than a
// capture or a compilation, which is why it carries the approvals and why an
// AI principal can author a draft of it but never approve it
// (ZTAX-CONT-REQ-0011, ZTAX-CONT-REQ-0012).
type Interpretation struct {
	ID                string
	SourceArtifacts   []string
	Facts             string
	AuthorityAnalysis string
	Legal             string
	// Ambiguity is the known unresolved questions. An interpretation with an
	// ambiguity is not refused — CONT-001 §2.5 makes ambiguity an explicit
	// state, not a defect — but it must be written down.
	Ambiguity         string
	ExecutableMapping string
	Impact            string
	VerificationRefs  []string
	LegalValidity     Window
	Status            InterpretationStatus
	Approvals         []Approval
	HighRiskAmbiguous bool
}

// Approve checks the interpretation may move to APPROVED and returns it so.
//
// It needs the four-eyes minimum (CONT-001 §8; ZTAX-CONT-REQ-0013), and for a
// high-risk or legally ambiguous change additionally a SENIOR_APPROVER
// (ZTAX-CONT-REQ-0014). It needs at least one source artifact
// (ZTAX-CONT-REQ-0001) and the verification references that will gate it
// (ZTAX-CONT-REQ-0016).
func (in Interpretation) Approve() (Interpretation, error) {
	if in.Status != InterpretationInReview {
		return in, errorf("interpretation %s is %s; only one IN_REVIEW is approved", in.ID, in.Status)
	}
	if len(in.SourceArtifacts) == 0 {
		return in, errorf("interpretation %s cites no source artifact (ZTAX-CONT-REQ-0001)", in.ID)
	}
	if len(in.VerificationRefs) == 0 {
		return in, errorf("interpretation %s names no verification (ZTAX-CONT-REQ-0016)", in.ID)
	}
	if err := in.LegalValidity.Validate(); err != nil {
		return in, errorf("interpretation %s: %v", in.ID, err)
	}
	if err := CheckFourEyes(in.Approvals, ""); err != nil {
		return in, errorf("interpretation %s: %v", in.ID, err)
	}
	if in.HighRiskAmbiguous && !hasRole(in.Approvals, RoleSeniorApprover) {
		return in, errorf("interpretation %s is high-risk or ambiguous and needs a %s (ZTAX-CONT-REQ-0014)", in.ID, RoleSeniorApprover)
	}
	in.Status = InterpretationApproved
	return in, nil
}

// RuleSemanticID is the stable identity of a logical rule across all of its
// versions (ADR-0012 §2.4). The DSL's `semantic` clause is one, and the
// compiler carries it onto every node so a trace names it.
type RuleSemanticID string

// Validate checks the identifier's shape: the ZTAX-RULE- prefix the estate's
// identifier scheme gives rules, then upper-case letters, digits and hyphens.
func (id RuleSemanticID) Validate() error {
	s := string(id)
	rest, ok := strings.CutPrefix(s, "ZTAX-RULE-")
	if !ok || rest == "" || strings.HasSuffix(rest, "-") {
		return errorf("rule semantic id %q must be ZTAX-RULE- followed by a name", s)
	}
	for _, c := range rest {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
			return errorf("rule semantic id %q may contain only upper-case letters, digits and hyphens", s)
		}
	}
	return nil
}

// RuleVersion is one effective-dated, knowledge-dated executable version of a
// rule (CONT-001 §3 "RuleDefinition", §15).
//
// It carries the five time concepts CONT-001 §9 forbids collapsing: legal
// validity, publication, knowledge, approval and deployment. A rule version
// with one effective_date would be unable to say whether a retroactive change
// was something the law did or something we learned late — and
// ZTAX-CONT-REQ-0081 requires the system to tell those apart.
type RuleVersion struct {
	SemanticID        RuleSemanticID
	Version           string
	LegalValidity     Window
	PublishedAt       time.Time
	KnownAt           time.Time
	ApprovedAt        time.Time
	DeploymentFrom    time.Time
	InterpretationRef string
	SourceArtifacts   []string
	TestRefs          []string
}

// Validate checks the version is identified, sourced and fully dated.
func (v RuleVersion) Validate() error {
	if err := v.SemanticID.Validate(); err != nil {
		return err
	}
	switch {
	case v.Version == "":
		return errorf("rule %s names no version (ZTAX-CONT-REQ-0009)", v.SemanticID)
	case v.InterpretationRef == "" || len(v.SourceArtifacts) == 0:
		return errorf("rule %s@%s must cite its interpretation and sources (ZTAX-CONT-REQ-0010)", v.SemanticID, v.Version)
	case v.KnownAt.IsZero() || v.ApprovedAt.IsZero():
		return errorf("rule %s@%s must record knowledge and approval time (ZTAX-CONT-REQ-0004)", v.SemanticID, v.Version)
	case v.ApprovedAt.Before(v.KnownAt):
		return errorf("rule %s@%s was approved before anyone knew of its source", v.SemanticID, v.Version)
	}
	return v.LegalValidity.Validate()
}

// ActiveAt reports whether the version may execute for a transaction at
// instant at: inside its legal window and not before its deployment instant
// (ZTAX-CONT-REQ-0025 — future-effective content is staged, never activated
// early).
func (v RuleVersion) ActiveAt(at time.Time) bool {
	return v.LegalValidity.Covers(at) && !at.Before(v.DeploymentFrom)
}

// RuleBundle is the content-model view of a compiled, signed bundle (CONT-001
// §3 "RuleBundle"): which pack it releases, and the exact digest a decision
// names. internal/domain/rule.Bundle is the executable form of the same thing;
// this is what the content plane and the evidence ledger say about it.
type RuleBundle struct {
	BundleID  string
	Digest    string
	IRVersion int
	Pack      PackID
	Version   Version
	Rules     []RuleRef
	Approvals []Approval
}

// RuleRef is a pinned reference to one rule version inside a bundle:
// semantic identity and exact version, never one without the other
// (ZTAX-DOM-001 §13.1).
type RuleRef struct {
	SemanticID RuleSemanticID
	Version    string
}
