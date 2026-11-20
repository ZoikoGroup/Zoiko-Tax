// Package rulechange models the rule approval queue: proposed changes to a
// jurisdiction rule pack, each citing the source it rests on, going through
// specialist review and independent approval (CONT-001 §3 ContentChange, §8
// four-eyes; Engineering Doc 03 §07 release state machine and permission map,
// WF-05 change intake, WF-06 interpretation; Sidebar Navigation Architecture
// WF-02 "Specialist rule review").
//
// The queue ends where content release begins. An approved change is
// APPROVED_FOR_RELEASE: eligible to be compiled, tested and signed into a
// content release by the release machinery (internal/domain/content, the
// bundle signer). Nothing here compiles a rule, signs a bundle or activates a
// pack — WF-02 "No direct release-pack publication", Doc 03 WF-05 "Auto-accept
// never publishes". So no act here changes a tax result: only authorized,
// reviewed, tested and signed content may (Doc 03 §01).
//
// The roles are separate people (Doc 03 §07; CONT-001 §8,
// ZTAX-CONT-REQ-0013):
//
//   - a proposer records the change, its source citation and its effective
//     date, and attaches the test evidence;
//   - a specialist reviewer who is not the proposer recommends it, asks for
//     changes or rejects it;
//   - an independent approver who is neither the proposer nor the
//     recommender approves it for release.
//
// A proposal may carry an opaque origin reference — a regulatory change feed
// item, an impact assessment, an AI-drafted recommendation. The reference is
// provenance only: a proposal's origin never approves it, and an AI may
// propose but no AI principal approves (CONT-001 §7, ZTAX-CONT-REQ-0011).
//
// Nothing is edited. A proposal's state is the fold of its events; a
// proposal sent back for changes is answered by a new proposal that
// supersedes it.
package rulechange

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/registration"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/remittance"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/sourcing"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// The reason codes.
var (
	// ReasonTestEvidenceRequired is a recommendation or approval of a change
	// with no test evidence attached (Doc 03 §07: TESTING / QA before
	// SIGNED; CONT-001 §8 "Verification").
	ReasonTestEvidenceRequired = errs.Register("RULE_CHANGE_TEST_EVIDENCE_REQUIRED", errs.CategoryPolicy,
		"content-rule-approvals",
		"A rule change is recommended and approved only with its test evidence attached. Record the golden-suite or regression evidence reference first.")

	// ReasonSourceAuthorityInsufficient is an approval of a change whose
	// cited source cannot by itself ground a production rule (CONT-001 §4,
	// ZTAX-CONT-REQ-0002: A0 and A1 may; everything below corroborates).
	ReasonSourceAuthorityInsufficient = errs.Register("SOURCE_AUTHORITY_INSUFFICIENT", errs.CategoryPolicy,
		"content-rule-approvals",
		"The cited source is not binding primary or official operational authority (A0 or A1), so it cannot ground a production rule by itself. Cite the governing instrument in a new proposal.")

	// ReasonVersionConflict is an act on a revision that is no longer
	// current.
	ReasonVersionConflict = registration.ReasonVersionConflict

	// ReasonMakerChecker is the estate's four-eyes code: here, the
	// conflict-of-interest rule.
	ReasonMakerChecker = remittance.ReasonMakerChecker
)

// Bounds.
const (
	MaxText    = 200
	MaxSummary = 500
	MaxDetail  = 8000
	MaxReason  = 2000
)

var (
	jurisdictionShape = regexp.MustCompile(`^[A-Z]{2}(-[A-Z0-9]{1,3})?$`)
	sha256Hex         = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ChangeType is what kind of legal effect the change encodes (Doc 03 WF-05
// "Change type: RATE_CHANGE / BASE_CHANGE ... multi-effect changes split or
// linked explicitly").
type ChangeType string

// The change types.
const (
	ChangeRate           ChangeType = "RATE_CHANGE"
	ChangeBase           ChangeType = "BASE_CHANGE"
	ChangeNewLevy        ChangeType = "NEW_LEVY"
	ChangeRepeal         ChangeType = "LEVY_REPEAL"
	ChangeExemption      ChangeType = "EXEMPTION_CHANGE"
	ChangeThreshold      ChangeType = "THRESHOLD_CHANGE"
	ChangeSitus          ChangeType = "SITUS_CHANGE"
	ChangeResponsibility ChangeType = "RESPONSIBILITY_CHANGE"
	ChangeObligation     ChangeType = "OBLIGATION_CHANGE"
	ChangeOther          ChangeType = "OTHER"
)

// ChangeTypes is every change type.
var ChangeTypes = []ChangeType{ChangeRate, ChangeBase, ChangeNewLevy, ChangeRepeal, ChangeExemption, ChangeThreshold,
	ChangeSitus, ChangeResponsibility, ChangeObligation, ChangeOther}

// Valid reports whether t is a known change type.
func (t ChangeType) Valid() bool {
	for _, k := range ChangeTypes {
		if k == t {
			return true
		}
	}
	return false
}

// Citation is the source a change rests on: the registered source, the exact
// passage, the edition read, and its authority class (CONT-001 §4).
type Citation struct {
	Source    sourcing.SourceID
	Reference string
	Edition   string
	Authority content.AuthorityClass
}

// TestEvidence is the reference to the test run a change was verified by:
// a golden-suite run, a regression diff (Doc 03 WF-08, WF-09). The digest of
// its report is optional and hashed in the browser.
type TestEvidence struct {
	Ref    string
	SHA256 string
}

// Validate checks the reference is named.
func (t TestEvidence) Validate() error {
	if err := text("testEvidence.ref", t.Ref, true, MaxText); err != nil {
		return err
	}
	if t.SHA256 != "" && !sha256Hex.MatchString(t.SHA256) {
		return errs.Invalid("testEvidence.sha256", errs.ReasonInvalidValue, "The report digest is its SHA-256 in lowercase hex.")
	}
	return nil
}

// Proposal is one proposed rule change and its history.
type Proposal struct {
	ID       id.RuleChangeID
	TenantID id.TenantID
	// Pack and BaseVersion are the rule pack and the version of it the
	// change is proposed against.
	Pack        content.PackID
	BaseVersion content.Version
	// Rule is the logical rule changed; empty for a change that adds a rule.
	Rule         content.RuleSemanticID
	Jurisdiction string
	ChangeType   ChangeType
	Summary      string
	// Detail is the proposed change itself: the before and after of the
	// rule, rate table or predicate, as the reviewer is to read it.
	Detail   string
	Impact   string
	Citation Citation
	// The three clocks Doc 03 WF-05 keeps apart: when the authority
	// published, when the law takes effect, and (CreatedAt) when Zoiko
	// recorded it.
	PublishedOn   time.Time
	EffectiveFrom time.Time
	EffectiveTo   time.Time // zero for open-ended; end-exclusive
	// OriginRef is an opaque reference to what prompted the proposal.
	OriginRef  string
	Supersedes id.RuleChangeID
	IntakeKey  string
	IntakeHash canonical.Digest
	CreatedAt  time.Time
	CreatedBy  id.UserID

	Status       Status
	TestEvidence *TestEvidence
	History      []Event
}

// Normalize trims what a person typed.
func (p Proposal) Normalize() Proposal {
	p.Jurisdiction = strings.ToUpper(strings.TrimSpace(p.Jurisdiction))
	p.Summary = strings.TrimSpace(p.Summary)
	p.Detail = strings.TrimSpace(p.Detail)
	p.Impact = strings.TrimSpace(p.Impact)
	p.OriginRef = strings.TrimSpace(p.OriginRef)
	p.Citation.Reference = strings.TrimSpace(p.Citation.Reference)
	p.Citation.Edition = strings.TrimSpace(p.Citation.Edition)
	p.Citation.Source = sourcing.SourceID(strings.TrimSpace(string(p.Citation.Source)))
	return p
}

// Validate checks a proposal before it is recorded: what it changes, the
// source it cites, and the legal dates (Doc 03 §10 "422 no source/effective
// date").
func (p Proposal) Validate() error {
	if err := p.Pack.Validate(); err != nil {
		return errs.Invalid("packId", errs.ReasonInvalidValue, "A pack id is upper-case letters, digits and hyphens, starting with a letter.")
	}
	if p.BaseVersion.IsZero() {
		return errs.Invalid("baseVersion", errs.ReasonMissingField, "Name the pack version the change is proposed against.")
	}
	if p.Rule != "" {
		if err := p.Rule.Validate(); err != nil {
			return errs.Invalid("ruleId", errs.ReasonInvalidValue, "A rule id is ZTAX-RULE- followed by upper-case letters, digits and hyphens.")
		}
	}
	if !jurisdictionShape.MatchString(p.Jurisdiction) {
		return errs.Invalid("jurisdiction", errs.ReasonInvalidValue, "A jurisdiction is a country, or a country and one subdivision: US, US-NY.")
	}
	if !p.ChangeType.Valid() {
		return errs.Invalid("changeType", errs.ReasonInvalidValue, "That is not a change type.")
	}
	for _, f := range []struct {
		name, value string
		required    bool
		limit       int
	}{
		{"summary", p.Summary, true, MaxSummary},
		{"detail", p.Detail, true, MaxDetail},
		{"impact", p.Impact, false, MaxDetail},
		{"originRef", p.OriginRef, false, MaxText},
		{"citation.sourceId", string(p.Citation.Source), true, MaxText},
		{"citation.reference", p.Citation.Reference, true, MaxText},
		{"citation.edition", p.Citation.Edition, true, MaxText},
	} {
		if err := text(f.name, f.value, f.required, f.limit); err != nil {
			return err
		}
	}
	if !p.Citation.Authority.Valid() {
		return errs.Invalid("citation.authorityClass", errs.ReasonInvalidValue, "The authority class is one of A0, A1, A2, B1, B2, C1 or U (CONT-001 §4).")
	}
	switch {
	case p.EffectiveFrom.IsZero():
		return errs.Invalid("effectiveFrom", errs.ReasonMissingField, "A rule change states when it takes legal effect.")
	case !p.EffectiveTo.IsZero() && !p.EffectiveTo.After(p.EffectiveFrom):
		return errs.Invalid("effectiveTo", errs.ReasonInvalidValue, "effectiveTo is after effectiveFrom (the window is end-exclusive).")
	case !p.PublishedOn.IsZero() && p.PublishedOn.After(p.CreatedAt):
		return errs.Invalid("publishedOn", errs.ReasonInvalidValue, "A source cannot be published after it was recorded.")
	}
	return nil
}

// Payload is what an intake request is hashed over for idempotency.
func (p Proposal) Payload() canonical.Value {
	opt := func(t time.Time) canonical.Value {
		if t.IsZero() {
			return canonical.Absent()
		}
		return canonical.String(t.Format(time.DateOnly))
	}
	sup := canonical.Absent()
	if !p.Supersedes.IsZero() {
		sup = canonical.String(p.Supersedes.String())
	}
	return canonical.Object(
		canonical.F("packId", canonical.String(string(p.Pack))),
		canonical.F("baseVersion", canonical.String(p.BaseVersion.String())),
		canonical.F("ruleId", canonical.OptString(string(p.Rule))),
		canonical.F("jurisdiction", canonical.String(p.Jurisdiction)),
		canonical.F("changeType", canonical.String(string(p.ChangeType))),
		canonical.F("summary", canonical.String(p.Summary)),
		canonical.F("detail", canonical.String(p.Detail)),
		canonical.F("impact", canonical.OptString(p.Impact)),
		canonical.F("citation", canonical.Object(
			canonical.F("sourceId", canonical.String(string(p.Citation.Source))),
			canonical.F("reference", canonical.String(p.Citation.Reference)),
			canonical.F("edition", canonical.String(p.Citation.Edition)),
			canonical.F("authorityClass", canonical.String(string(p.Citation.Authority))),
		)),
		canonical.F("publishedOn", opt(p.PublishedOn)),
		canonical.F("effectiveFrom", opt(p.EffectiveFrom)),
		canonical.F("effectiveTo", opt(p.EffectiveTo)),
		canonical.F("originRef", canonical.OptString(p.OriginRef)),
		canonical.F("supersedes", sup),
	)
}

// ValidJurisdiction reports whether j is a country, or a country and one
// subdivision.
func ValidJurisdiction(j string) bool { return jurisdictionShape.MatchString(j) }

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
