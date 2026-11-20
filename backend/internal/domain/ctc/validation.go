package ctc

import (
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Format validation (Doc 21 W06 Format Profile & Semantic Validation).
//
// A committed fiscal document is checked against the format profile of the
// applicability record it is to be submitted under. The rules are code,
// versioned together as Ruleset: the same document, record version and
// ruleset always give the same results, and a report is never edited — a
// new ruleset, record version or document state is a new report.
//
// What is checked is what the committed fiscal document can answer: the
// record's cell, the canonical invoice semantics (number, lines, tax pinned
// to decisions, exact totals, the preceding-document reference of a
// correction) and the structural constraints the national profiles put on
// those fields. The official schema and Schematron stage needs the licensed
// validation bundle of the profile's version, which is not installed: that
// stage is reported NOT_EVALUATED, never PASS. A PASSED report is therefore a
// pre-dispatch structural check, not certification (Doc 21 W06 "a pass in
// generic Peppol tests never means KSeF/NFCom/FATOORA compliance").

// Ruleset is the version of the rules below. Any change to a rule's logic,
// severity or reason is a new version.
const Ruleset = "ztax-ctc-structural/2026.11.1"

// The rule reason codes a failing or unevaluated rule carries.
var (
	ReasonRuleEntityMismatch = errs.Register("CTC_RULE_ENTITY_MISMATCH", errs.CategoryValidation, "g-030",
		"The document's legal entity is not the applicability record's.")
	ReasonRuleTypeMismatch = errs.Register("CTC_RULE_TYPE_MISMATCH", errs.CategoryValidation, "g-030",
		"The document's type is not the applicability record's.")
	ReasonRuleOutOfEffect = errs.Register("CTC_RULE_OUT_OF_EFFECT", errs.CategoryValidation, "g-030",
		"The document's issue date is outside the applicability record's effective dates.")
	ReasonRuleNotCommitted = errs.Register("CTC_RULE_NOT_COMMITTED", errs.CategoryValidation, "g-030",
		"The document is not committed fiscal history. Only a committed document is projected to an electronic document.")
	ReasonRuleNumberMissing = errs.Register("CTC_RULE_NUMBER_MISSING", errs.CategoryValidation, "g-030",
		"The document has no legal document number.")
	ReasonRuleNumberFormat = errs.Register("CTC_RULE_NUMBER_FORMAT", errs.CategoryValidation, "g-030",
		"The legal document number does not meet the profile's length or character constraints.")
	ReasonRuleNoLines = errs.Register("CTC_RULE_NO_LINES", errs.CategoryValidation, "g-030",
		"The document has no lines.")
	ReasonRuleLineUntraced = errs.Register("CTC_RULE_LINE_UNTRACED", errs.CategoryValidation, "g-030",
		"A line carries no source line reference, so its provenance to the billing line is not shown.")
	ReasonRuleTaxUnpinned = errs.Register("CTC_RULE_TAX_UNPINNED", errs.CategoryValidation, "g-030",
		"A tax line names no committed tax decision.")
	ReasonRuleTotalsMismatch = errs.Register("CTC_RULE_TOTALS_MISMATCH", errs.CategoryValidation, "g-030",
		"The lines do not sum exactly to the document's committed totals. The difference is reported, never absorbed.")
	ReasonRulePrecedingReference = errs.Register("CTC_RULE_PRECEDING_REFERENCE_MISSING", errs.CategoryValidation, "g-030",
		"A correcting document names no preceding document.")
	ReasonRuleCurrencyNotPermitted = errs.Register("CTC_RULE_CURRENCY_NOT_PERMITTED", errs.CategoryValidation, "g-030",
		"The profile requires amounts in a currency the document does not carry.")
	ReasonRuleSchemaNotEvaluated = errs.Register("CTC_RULE_SCHEMA_NOT_EVALUATED", errs.CategoryUnsupported, "g-030",
		"The official schema and Schematron stage was not run: no licensed validation bundle for the profile's version is installed. This is not a pass.")
)

// Stage is the validation stage a rule belongs to (Doc 21 W06 "syntax /
// semantic / regime validation stages").
type Stage string

// The stages.
const (
	StageApplicability Stage = "APPLICABILITY"
	StageSemantic      Stage = "SEMANTIC"
	StageRegime        Stage = "REGIME"
	StageSyntax        Stage = "SYNTAX"
)

// Severity is what a failing rule does: FATAL blocks submission and is
// never waived operationally; WARNING is reported and does not block.
type Severity string

// The severities.
const (
	SeverityFatal   Severity = "FATAL"
	SeverityWarning Severity = "WARNING"
)

// RuleOutcome is one rule's result.
type RuleOutcome string

// The rule outcomes.
const (
	RulePass         RuleOutcome = "PASS"
	RuleFail         RuleOutcome = "FAIL"
	RuleNotEvaluated RuleOutcome = "NOT_EVALUATED"
)

// RuleResult is one rule's result on one document.
type RuleResult struct {
	RuleID   string
	Stage    Stage
	Severity Severity
	Outcome  RuleOutcome
	// Reason is the failing or unevaluated rule's code; empty on a pass.
	Reason errs.ReasonCode
	// BusinessTerm is the field the rule is about, in the profile's terms.
	BusinessTerm string
	Detail       string
}

// Outcome is a report's overall result.
type Outcome string

// The outcomes. PASSED means no FATAL rule failed; it is not certification.
const (
	OutcomePassed Outcome = "PASSED"
	OutcomeFailed Outcome = "FAILED"
)

// DocumentInput is the committed fiscal document as stored: the document,
// its current lifecycle status and the totals computed when it was
// committed.
type DocumentInput struct {
	Document document.Document
	Status   document.Status
	Net      fiscal.Money
	Tax      fiscal.Money
	Gross    fiscal.Money
}

// Projection is the canonical content of the document an electronic
// document is projected from: header, lines, taxes and committed totals.
// Its digest is the snapshot hash a validation and a submission are bound
// to.
func (in DocumentInput) Projection() canonical.Value {
	d := in.Document
	preds := make([]canonical.Value, len(d.Predecessors))
	for i, p := range d.Predecessors {
		preds[i] = canonical.String(p.String())
	}
	lines := make([]canonical.Value, len(d.Lines))
	for i, l := range d.Lines {
		taxes := make([]canonical.Value, len(l.Taxes))
		for j, t := range l.Taxes {
			taxes[j] = canonical.Object(
				canonical.F("decisionId", canonical.String(t.Decision.String())),
				canonical.F("component", canonical.String(t.Component)),
				canonical.F("amount", canonical.Money(t.Amount)),
			)
		}
		pred := canonical.Absent()
		if l.Predecessor != nil {
			pred = canonical.String(l.Predecessor.String())
		}
		lines[i] = canonical.Object(
			canonical.F("lineId", canonical.String(l.ID.String())),
			canonical.F("sourceLineRef", canonical.OptString(l.SourceLineRef)),
			canonical.F("componentInstance", canonical.OptString(l.ComponentInstance)),
			canonical.F("net", canonical.Money(l.Net)),
			canonical.F("predecessor", pred),
			canonical.F("taxes", canonical.Array(taxes...)),
		)
	}
	return canonical.Object(
		canonical.F("artifact", canonical.String("ctc-document-projection")),
		canonical.F("documentId", canonical.String(d.ID.String())),
		canonical.F("legalEntityId", canonical.String(d.LegalEntity.String())),
		canonical.F("type", canonical.String(string(d.Type))),
		canonical.F("number", canonical.OptString(d.Number)),
		canonical.F("rootId", canonical.String(d.Root.String())),
		canonical.F("predecessors", canonical.Array(preds...)),
		canonical.F("issueDate", canonical.String(d.IssueDate.UTC().Format(time.DateOnly))),
		canonical.F("taxPoint", canonical.Time(d.TaxPoint)),
		canonical.F("currency", canonical.String(string(d.Currency))),
		canonical.F("lines", canonical.Array(lines...)),
		canonical.F("net", canonical.Money(in.Net)),
		canonical.F("tax", canonical.Money(in.Tax)),
		canonical.F("gross", canonical.Money(in.Gross)),
	)
}

// InputDigest is the digest of the document's projection and its current
// lifecycle status: what a validation's results depend on.
func (in DocumentInput) InputDigest() (canonical.Digest, error) {
	return canonical.Sum(canonical.Object(
		canonical.F("projection", in.Projection()),
		canonical.F("status", canonical.String(string(in.Status))),
	))
}

// Validation is one immutable validation report.
type Validation struct {
	ID                   id.CTCValidationID
	TenantID             id.TenantID
	Document             id.FiscalDocumentID
	Applicability        id.CTCApplicabilityID
	ApplicabilityVersion int
	Profile              Profile
	FormatVersion        string
	Ruleset              string
	InputDigest          canonical.Digest
	Results              []RuleResult
	Outcome              Outcome
	// Digest seals everything above.
	Digest      canonical.Digest
	ValidatedAt time.Time
	ValidatedBy id.UserID
}

// Failures counts the FATAL rules that failed.
func (v Validation) Failures() int {
	n := 0
	for _, r := range v.Results {
		if r.Outcome == RuleFail && r.Severity == SeverityFatal {
			n++
		}
	}
	return n
}

// Payload is the canonical report the digest covers.
func (v Validation) Payload() canonical.Value {
	results := make([]canonical.Value, len(v.Results))
	for i, r := range v.Results {
		results[i] = canonical.Object(
			canonical.F("ruleId", canonical.String(r.RuleID)),
			canonical.F("stage", canonical.String(string(r.Stage))),
			canonical.F("severity", canonical.String(string(r.Severity))),
			canonical.F("outcome", canonical.String(string(r.Outcome))),
			canonical.F("reason", canonical.OptString(string(r.Reason))),
			canonical.F("businessTerm", canonical.OptString(r.BusinessTerm)),
			canonical.F("detail", canonical.OptString(r.Detail)),
		)
	}
	return canonical.Object(
		canonical.F("artifact", canonical.String("ctc-validation-report")),
		canonical.F("validationId", canonical.String(v.ID.String())),
		canonical.F("documentId", canonical.String(v.Document.String())),
		canonical.F("applicabilityId", canonical.String(v.Applicability.String())),
		canonical.F("applicabilityVersion", canonical.Integer(int64(v.ApplicabilityVersion))),
		canonical.F("profile", canonical.String(string(v.Profile))),
		canonical.F("formatVersion", canonical.OptString(v.FormatVersion)),
		canonical.F("ruleset", canonical.String(v.Ruleset)),
		canonical.F("inputDigest", canonical.String(v.InputDigest.String())),
		canonical.F("results", canonical.Array(results...)),
		canonical.F("outcome", canonical.String(string(v.Outcome))),
	)
}

// Verify recomputes the report digest from what the report holds.
func (v Validation) Verify() error {
	d, err := canonical.Sum(v.Payload())
	if err != nil {
		return err
	}
	if !d.Equal(v.Digest) {
		return fmt.Errorf("ctc: validation %s holds a report whose digest is %s, not its sealed %s", v.ID, d, v.Digest)
	}
	return nil
}

// Current reports whether the report still speaks for the document under the
// record as it now is: the same document state, record version and ruleset.
func (v Validation) Current(inputDigest canonical.Digest, a Applicability) bool {
	return v.InputDigest.Equal(inputDigest) && v.Applicability == a.ID && v.ApplicabilityVersion == a.Current().Number &&
		v.Ruleset == Ruleset
}

// ValidateInput is what a validation runs over.
type ValidateInput struct {
	ID            id.CTCValidationID
	TenantID      id.TenantID
	Document      DocumentInput
	Applicability Applicability
	At            time.Time
	By            id.UserID
}

// Validate runs the ruleset over a document under a record's current terms
// and seals the report. The record must be ACTIVE and dispatch documents: a
// PROHIBITED record has no profile to validate against.
func Validate(in ValidateInput) (Validation, error) {
	a := in.Applicability
	if a.Status != StatusActive {
		return Validation{}, errs.New(errs.CategoryConflict, ReasonApplicabilityNotActive,
			fmt.Sprintf("Record %s is %s. Documents are validated under an active record.", a.ID, a.Status))
	}
	cur := a.Current()
	if !cur.Terms.Duty.Dispatches() {
		return Validation{}, errs.New(errs.CategoryConflict, ReasonSubmissionProhibited,
			fmt.Sprintf("Record %s says this document is PROHIBITED; it has no format profile to validate against.", a.ID))
	}
	digest, err := in.Document.InputDigest()
	if err != nil {
		return Validation{}, err
	}
	results, err := rules(in.Document, a)
	if err != nil {
		return Validation{}, err
	}
	v := Validation{
		ID: in.ID, TenantID: in.TenantID, Document: in.Document.Document.ID, Applicability: a.ID,
		ApplicabilityVersion: cur.Number, Profile: cur.Terms.Profile, FormatVersion: cur.Terms.FormatVersion,
		Ruleset: Ruleset, InputDigest: digest, Results: results, Outcome: OutcomePassed,
		ValidatedAt: in.At.UTC(), ValidatedBy: in.By,
	}
	if v.Failures() > 0 {
		v.Outcome = OutcomeFailed
	}
	if v.Digest, err = canonical.Sum(v.Payload()); err != nil {
		return Validation{}, err
	}
	return v, nil
}

// correcting are the types that always name the document they correct
// (EN 16931 BG-3, preceding invoice reference).
var correcting = []document.Type{document.TypeCreditNote, document.TypeRebill}

var (
	itNumber = regexp.MustCompile(`^.{1,20}$`)
	brNumber = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
	inNumber = regexp.MustCompile(`^[A-Za-z1-9][A-Za-z0-9/-]{0,15}$`)
	plNumber = regexp.MustCompile(`^.{1,256}$`)
)

func check(id string, stage Stage, sev Severity, term string, ok bool, reason errs.ReasonCode, detail string) RuleResult {
	r := RuleResult{RuleID: id, Stage: stage, Severity: sev, Outcome: RulePass, BusinessTerm: term}
	if !ok {
		r.Outcome, r.Reason, r.Detail = RuleFail, reason, detail
	}
	return r
}

// rules is the ruleset, in a fixed order.
func rules(in DocumentInput, a Applicability) ([]RuleResult, error) {
	d, t := in.Document, a.Current().Terms
	var out []RuleResult
	add := func(r RuleResult) { out = append(out, r) }

	// Applicability: the document belongs to the record's cell.
	add(check("ZT-CTC-A01", StageApplicability, SeverityFatal, "Seller legal entity", d.LegalEntity == a.LegalEntity,
		ReasonRuleEntityMismatch, fmt.Sprintf("The document is legal entity %s's; the record is %s's.", d.LegalEntity, a.LegalEntity)))
	add(check("ZT-CTC-A02", StageApplicability, SeverityFatal, "Document type", d.Type == a.DocumentType,
		ReasonRuleTypeMismatch, fmt.Sprintf("The document is a %s; the record covers %s.", d.Type, a.DocumentType)))
	add(check("ZT-CTC-A03", StageApplicability, SeverityFatal, "Issue date (BT-2)", t.InEffect(d.IssueDate),
		ReasonRuleOutOfEffect, fmt.Sprintf("Issued %s; the record's terms are in effect %s.", d.IssueDate.UTC().Format(time.DateOnly), window(t))))

	// Semantic: canonical invoice semantics the committed document answers.
	committed := in.Status != document.StatusDraft && in.Status != document.StatusValidated
	add(check("ZT-CTC-S01", StageSemantic, SeverityFatal, "Fiscal commitment", committed,
		ReasonRuleNotCommitted, fmt.Sprintf("The document is %s.", in.Status)))
	add(check("ZT-CTC-S02", StageSemantic, SeverityFatal, "Invoice number (BT-1)", d.Number != "",
		ReasonRuleNumberMissing, "The document carries no legal number; an authority-assigned number is recorded on the fiscal document first."))
	add(check("ZT-CTC-S03", StageSemantic, SeverityFatal, "Invoice line (BG-25)", len(d.Lines) > 0,
		ReasonRuleNoLines, "The document has no lines."))
	untraced, unpinned := 0, 0
	for _, l := range d.Lines {
		if l.SourceLineRef == "" {
			untraced++
		}
		for _, tx := range l.Taxes {
			if tx.Decision.IsZero() {
				unpinned++
			}
		}
	}
	add(check("ZT-CTC-S04", StageSemantic, SeverityWarning, "Source line reference", untraced == 0,
		ReasonRuleLineUntraced, fmt.Sprintf("%d of %d lines carry no source line reference.", untraced, len(d.Lines))))
	add(check("ZT-CTC-S05", StageSemantic, SeverityFatal, "Tax decision reference", unpinned == 0,
		ReasonRuleTaxUnpinned, fmt.Sprintf("%d tax lines name no committed decision.", unpinned)))
	totals, err := totalsCheck(in)
	if err != nil {
		return nil, err
	}
	add(totals)
	needsPreceding := slices.Contains(correcting, d.Type)
	add(check("ZT-CTC-S07", StageSemantic, SeverityFatal, "Preceding invoice reference (BG-3)",
		!needsPreceding || len(d.Predecessors) > 0, ReasonRulePrecedingReference,
		fmt.Sprintf("A %s names the document it corrects.", d.Type)))

	// Regime: the national profiles' structural constraints on those fields.
	switch t.Profile {
	case ProfileITFatturaPA:
		add(check("ZT-CTC-IT01", StageRegime, SeverityFatal, "FatturaPA 2.1.1.4 Numero", itNumber.MatchString(d.Number),
			ReasonRuleNumberFormat, "FatturaPA's document number is 1 to 20 characters."))
	case ProfileBRNFCom62:
		add(check("ZT-CTC-BR01", StageRegime, SeverityFatal, "NFCom ide/nNF", brNumber.MatchString(d.Number),
			ReasonRuleNumberFormat, "The NFCom number is 1 to 9 digits, not starting with zero."))
		add(check("ZT-CTC-BR02", StageRegime, SeverityFatal, "NFCom amounts", d.Currency == "BRL",
			ReasonRuleCurrencyNotPermitted, fmt.Sprintf("NFCom amounts are in BRL; the document is in %s.", d.Currency)))
	case ProfileINGSTIRP:
		add(check("ZT-CTC-IN01", StageRegime, SeverityFatal, "IRP DocDtls/No", inNumber.MatchString(d.Number),
			ReasonRuleNumberFormat, "The document number is 1 to 16 letters, digits, / or -, not starting with 0, / or -."))
		add(check("ZT-CTC-IN02", StageRegime, SeverityFatal, "IRP ValDtls", d.Currency == "INR",
			ReasonRuleCurrencyNotPermitted, fmt.Sprintf("IRP values are reported in INR; the document is in %s.", d.Currency)))
	case ProfilePLKSeFFA:
		add(check("ZT-CTC-PL01", StageRegime, SeverityFatal, "KSeF FA P_2", plNumber.MatchString(d.Number),
			ReasonRuleNumberFormat, "The KSeF invoice number is 1 to 256 characters."))
	case ProfileSAZATCAUBL:
		add(check("ZT-CTC-SA01", StageRegime, SeverityFatal, "Tax total in SAR (BT-111)", d.Currency == "SAR",
			ReasonRuleCurrencyNotPermitted, fmt.Sprintf("A tax total in SAR is required; the document is in %s and carries no SAR tax total.", d.Currency)))
	}

	// Syntax: the official schema and Schematron, which are not installed.
	add(RuleResult{RuleID: "ZT-CTC-X01", Stage: StageSyntax, Severity: SeverityFatal, Outcome: RuleNotEvaluated,
		Reason: ReasonRuleSchemaNotEvaluated, BusinessTerm: fmt.Sprintf("%s %s", t.Profile, t.FormatVersion),
		Detail: "No licensed schema or Schematron bundle for this profile version is installed. Syntax validation belongs to the accredited adapter and is not claimed here."})
	return out, nil
}

func window(t Terms) string {
	if t.EffectiveTo.IsZero() {
		return "from " + t.EffectiveFrom.Format(time.DateOnly)
	}
	return t.EffectiveFrom.Format(time.DateOnly) + " to " + t.EffectiveTo.Format(time.DateOnly)
}

// totalsCheck is Doc 21 §06's mathematical control: the lines' net and tax
// sum exactly to the committed totals, and net plus tax is the gross. Any
// difference, even a cent, fails with the exact figures.
func totalsCheck(in DocumentInput) (RuleResult, error) {
	d := in.Document
	net, err := fiscal.ParseMoney("0", d.Currency)
	if err != nil {
		return RuleResult{}, err
	}
	tax := net
	for _, l := range d.Lines {
		if net, err = net.Add(l.Net); err != nil {
			return RuleResult{}, err
		}
		for _, t := range l.Taxes {
			if tax, err = tax.Add(t.Amount); err != nil {
				return RuleResult{}, err
			}
		}
	}
	gross, err := in.Net.Add(in.Tax)
	if err != nil {
		return RuleResult{}, err
	}
	var problems []string
	compare := func(name string, got, want fiscal.Money) error {
		c, err := got.Cmp(want)
		if err != nil {
			return err
		}
		if c != 0 {
			diff, err := got.Sub(want)
			if err != nil {
				return err
			}
			problems = append(problems, fmt.Sprintf("%s: lines give %s, committed %s (difference %s)", name,
				got.CanonicalString(), want.CanonicalString(), diff.CanonicalString()))
		}
		return nil
	}
	if err := compare("net (BT-106)", net, in.Net); err != nil {
		return RuleResult{}, err
	}
	if err := compare("tax (BT-110)", tax, in.Tax); err != nil {
		return RuleResult{}, err
	}
	if err := compare("gross (BT-112)", gross, in.Gross); err != nil {
		return RuleResult{}, err
	}
	detail := ""
	for i, p := range problems {
		if i > 0 {
			detail += "; "
		}
		detail += p
	}
	return check("ZT-CTC-S06", StageSemantic, SeverityFatal, "Document totals (BT-106, BT-110, BT-112)", len(problems) == 0,
		ReasonRuleTotalsMismatch, detail+"."), nil
}

// ValidationFilter narrows a listing of reports. Empty fields do not filter.
type ValidationFilter struct {
	Document      id.FiscalDocumentID
	Applicability id.CTCApplicabilityID
}
