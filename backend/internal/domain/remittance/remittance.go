// Package remittance models paying what a filing established is owed
// (ZTAX-REM-001, W4 lane M).
//
// Three things are kept apart here because confusing them is how a tax
// payment goes wrong:
//
//   - The payee. A Beneficiary is controlled master data: versioned, never
//     edited, and usable only once someone other than its author has
//     verified it (ZTAX-REM-REQ-0012, -0014, -0015). A payment pins the exact
//     version it was approved against (ZTAX-REM-REQ-0017).
//   - The instruction. A Remittance is immutable from the moment it is made:
//     amount, currency, payee version, funding reference, payment reference
//     and execution date are hashed into its instruction hash, and approval
//     binds that hash (ZTAX-REM-REQ-0026, -0033, -0036). A different payment
//     is a different remittance.
//   - The outcome. SUBMITTED is not ACCEPTED_BY_PROVIDER, which is not
//     SETTLED; only settlement evidence settles, and an ambiguous answer is
//     UNCERTAIN_PAYMENT, which only a person resolves
//     (ZTAX-REM-REQ-0037 to -0044).
//
// ZoikoTax never holds the money. A funding account is a reference to the
// customer's own account at a regulated institution, and every operating
// mode here instructs or records; none takes custody (ZTAX-REM-REQ-0001,
// -0009).
package remittance

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// The package's own reason codes.
var (
	// ReasonMakerChecker is a person approving or verifying what they made
	// themselves (ZTAX-REM-REQ-0014, -0024, -0031).
	ReasonMakerChecker = errs.Register("MAKER_CHECKER", errs.CategoryPolicy,
		"lane-m",
		"The person who made this cannot also approve or verify it. Another person must.")

	// ReasonInstructionChanged is an approval naming a hash that is not the
	// instruction's (ZTAX-REM-REQ-0026).
	ReasonInstructionChanged = errs.Register("INSTRUCTION_HASH_MISMATCH", errs.CategoryConflict,
		"lane-m",
		"The instruction hash approved is not this instruction's. Approval binds the exact instruction.")

	// ReasonPaymentBlocked is a payment a readiness check refuses
	// (ZTAX-REM-REQ-0015, -0018 to -0023).
	ReasonPaymentBlocked = errs.Register("PAYMENT_BLOCKED", errs.CategoryPolicy,
		"lane-m",
		"This payment may not proceed now. The detail says which readiness check failed.")
)

// ---------------------------------------------------------------------------
// beneficiaries
// ---------------------------------------------------------------------------

// Rail is how money reaches the payee.
type Rail string

// The rails (ZTAX-REM-001 §7).
const (
	RailACH           Rail = "ACH"
	RailWire          Rail = "WIRE"
	RailSEPA          Rail = "SEPA"
	RailRTGS          Rail = "RTGS"
	RailLocalTransfer Rail = "LOCAL_TRANSFER"
	RailPortal        Rail = "PORTAL"
	RailDirectDebit   Rail = "DIRECT_DEBIT"
	RailOther         Rail = "OTHER"
)

// Valid reports whether the rail is one of §7's.
func (r Rail) Valid() bool {
	switch r {
	case RailACH, RailWire, RailSEPA, RailRTGS, RailLocalTransfer, RailPortal, RailDirectDebit, RailOther:
		return true
	}
	return false
}

// BeneficiaryState is where a beneficiary version stands.
type BeneficiaryState string

// The beneficiary states. CHANGE_PENDING is an unverified version that
// replaces a verified one (ZTAX-REM-001 §7).
const (
	BeneficiaryUnverified    BeneficiaryState = "UNVERIFIED"
	BeneficiaryChangePending BeneficiaryState = "CHANGE_PENDING"
	BeneficiaryVerified      BeneficiaryState = "VERIFIED"
	BeneficiarySuspended     BeneficiaryState = "SUSPENDED"
	BeneficiarySuperseded    BeneficiaryState = "SUPERSEDED"
)

// BeneficiaryEventKind is what happened to a beneficiary version.
type BeneficiaryEventKind string

// The beneficiary events.
const (
	BeneficiaryCreated   BeneficiaryEventKind = "CREATED"
	BeneficiaryVerify    BeneficiaryEventKind = "VERIFIED"
	BeneficiarySuspend   BeneficiaryEventKind = "SUSPENDED"
	BeneficiarySupersede BeneficiaryEventKind = "SUPERSEDED"
)

// BeneficiaryEvent is one act on a beneficiary version.
type BeneficiaryEvent struct {
	Seq        int
	Kind       BeneficiaryEventKind
	Evidence   string
	RecordedAt time.Time
	RecordedBy id.UserID
}

// secureRef is the shape of a reference to an account held elsewhere: a
// vault path or a provider token, never an account number
// (ZTAX-REM-REQ-0011).
var secureRef = regexp.MustCompile(`^(vault|awssm|gcpsm|azkv|token)://[A-Za-z0-9._/-]{1,240}$`)

// IsSecureRef reports whether s is a reference rather than a raw secret or
// account number.
func IsSecureRef(s string) bool { return secureRef.MatchString(s) }

var currencyShape = regexp.MustCompile(`^[A-Z]{3}$`)

// Beneficiary is one version of an authority's payee details, folded from
// its events.
type Beneficiary struct {
	ID       id.BeneficiaryID
	TenantID id.TenantID
	// Supersedes is the version this one replaces, zero for the first.
	Supersedes id.BeneficiaryID

	Country   string
	Authority string
	LegalName string
	Rail      Rail
	// AccountRef is where the payee's account details are held, by
	// reference only.
	AccountRef string
	// ReferenceRule is the authority's payment-reference format, as a
	// regular expression; empty when the authority prescribes none.
	ReferenceRule string
	Currencies    []string
	// Source is the official instruction the details were taken from.
	Source string

	CreatedAt time.Time
	CreatedBy id.UserID

	State   BeneficiaryState
	History []BeneficiaryEvent
}

// Validate refuses a beneficiary version that cannot be recorded.
func (b Beneficiary) Validate() error {
	switch {
	case len(b.Country) != 2 || strings.ToUpper(b.Country) != b.Country:
		return fmt.Errorf("remittance: beneficiary country is an ISO 3166-1 alpha-2 code")
	case strings.TrimSpace(b.Authority) == "" || strings.TrimSpace(b.LegalName) == "":
		return fmt.Errorf("remittance: beneficiary names its authority and legal name")
	case !b.Rail.Valid():
		return fmt.Errorf("remittance: payment rail %q is not one this service knows", b.Rail)
	case !IsSecureRef(b.AccountRef):
		return fmt.Errorf("remittance: the account is named by a vault or token reference, never by its number")
	case len(b.Currencies) == 0:
		return fmt.Errorf("remittance: beneficiary accepts at least one currency")
	case strings.TrimSpace(b.Source) == "":
		return fmt.Errorf("remittance: beneficiary details name the official source they were taken from")
	}
	for _, c := range b.Currencies {
		if !currencyShape.MatchString(c) {
			return fmt.Errorf("remittance: %q is not an ISO 4217 currency code", c)
		}
	}
	if b.ReferenceRule != "" {
		if _, err := regexp.Compile(b.ReferenceRule); err != nil {
			return fmt.Errorf("remittance: the payment reference rule is not a valid pattern")
		}
	}
	return nil
}

// FoldBeneficiary applies a version's events in order.
func FoldBeneficiary(b Beneficiary, events []BeneficiaryEvent) (Beneficiary, error) {
	if len(events) == 0 || events[0].Kind != BeneficiaryCreated || events[0].Seq != 1 {
		return Beneficiary{}, fmt.Errorf("remittance: a beneficiary's history starts with its CREATED event")
	}
	b.State = BeneficiaryUnverified
	if !b.Supersedes.IsZero() {
		b.State = BeneficiaryChangePending
	}
	for i, e := range events[1:] {
		if e.Seq != i+2 {
			return Beneficiary{}, fmt.Errorf("remittance: beneficiary event %d is out of sequence", e.Seq)
		}
		switch e.Kind {
		case BeneficiaryVerify:
			if b.State != BeneficiaryUnverified && b.State != BeneficiaryChangePending {
				return Beneficiary{}, fmt.Errorf("remittance: a %s beneficiary was verified", b.State)
			}
			b.State = BeneficiaryVerified
		case BeneficiarySuspend:
			if b.State == BeneficiarySuperseded {
				return Beneficiary{}, fmt.Errorf("remittance: a superseded beneficiary was suspended")
			}
			b.State = BeneficiarySuspended
		case BeneficiarySupersede:
			b.State = BeneficiarySuperseded
		default:
			return Beneficiary{}, fmt.Errorf("remittance: beneficiary event %q", e.Kind)
		}
	}
	b.History = events
	return b, nil
}

// Verify records an independent verification of this version against its
// official source (ZTAX-REM-REQ-0014). The person who entered the details
// does not verify them.
func (b Beneficiary) Verify(evidence string, by id.UserID, at time.Time) (BeneficiaryEvent, error) {
	if b.State != BeneficiaryUnverified && b.State != BeneficiaryChangePending {
		return BeneficiaryEvent{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only an unverified beneficiary is verified; this one is %s.", b.State))
	}
	if by == b.CreatedBy {
		return BeneficiaryEvent{}, errs.New(errs.CategoryPolicy, ReasonMakerChecker,
			"The person who entered these payee details cannot verify them (ZTAX-REM-REQ-0014).")
	}
	if strings.TrimSpace(evidence) == "" {
		return BeneficiaryEvent{}, errs.Invalid("evidence", errs.ReasonMissingField,
			"A verification says how the details were confirmed against the authority's official source.")
	}
	return BeneficiaryEvent{Seq: len(b.History) + 1, Kind: BeneficiaryVerify, Evidence: evidence, RecordedAt: at.UTC(), RecordedBy: by}, nil
}

// Payable reports whether a payment may be made to this version in this
// currency, and why not (ZTAX-REM-REQ-0015).
func (b Beneficiary) Payable(currency fiscal.Currency) (bool, string) {
	switch {
	case b.State != BeneficiaryVerified:
		return false, fmt.Sprintf("The beneficiary version is %s; only a verified payee is paid.", b.State)
	case !slices.Contains(b.Currencies, string(currency)):
		return false, fmt.Sprintf("The beneficiary does not accept %s.", currency)
	}
	return true, "The payee version is verified and accepts the currency."
}

// ---------------------------------------------------------------------------
// remittances
// ---------------------------------------------------------------------------

// Mode is who executes the payment (ZTAX-REM-001 §3). None of them takes
// custody of funds.
type Mode string

// The operating models.
const (
	ModeCustomerExecutes  Mode = "CUSTOMER_EXECUTES"
	ModeBankAPIInitiation Mode = "BANK_API_INITIATION"
	ModeTreasuryPlatform  Mode = "TREASURY_PLATFORM"
	ModeDirectDebit       Mode = "DIRECT_DEBIT_AUTHORITY"
	ModeLocalPartner      Mode = "LOCAL_PARTNER"
	ModeManaged           Mode = "MANAGED_ORCHESTRATION"
	ModeOfflineManual     Mode = "OFFLINE_MANUAL"
)

// Valid reports whether the mode is one of §3's.
func (m Mode) Valid() bool {
	switch m {
	case ModeCustomerExecutes, ModeBankAPIInitiation, ModeTreasuryPlatform, ModeDirectDebit, ModeLocalPartner,
		ModeManaged, ModeOfflineManual:
		return true
	}
	return false
}

// Status is where a payment is (ZTAX-REM-001 §10).
type Status string

// The statuses.
const (
	StatusApprovalRequired Status = "APPROVAL_REQUIRED"
	StatusApproved         Status = "APPROVED"
	StatusSubmitted        Status = "SUBMITTED"
	StatusAccepted         Status = "ACCEPTED_BY_PROVIDER"
	StatusProcessing       Status = "PROCESSING"
	StatusUncertain        Status = "UNCERTAIN_PAYMENT"
	StatusSettled          Status = "SETTLED"
	StatusRejected         Status = "REJECTED"
	StatusReturned         Status = "RETURNED"
	StatusCancelled        Status = "CANCELLED"
)

// Terminal reports whether nothing further happens to the payment.
func (s Status) Terminal() bool {
	return s == StatusRejected || s == StatusReturned || s == StatusCancelled
}

// Sent reports whether the instruction has left, or may have left, for the
// provider.
func (s Status) Sent() bool {
	switch s {
	case StatusSubmitted, StatusAccepted, StatusProcessing, StatusUncertain, StatusSettled, StatusReturned:
		return true
	}
	return false
}

// Evidence is the kind of proof a provider status carries
// (ZTAX-REM-001 §18).
type Evidence string

// The evidence kinds.
const (
	EvidenceProviderAcceptance Evidence = "PROVIDER_ACCEPTANCE"
	EvidenceProcessingRef      Evidence = "PROCESSING_REFERENCE"
	EvidenceAccountDebit       Evidence = "ACCOUNT_DEBIT"
	EvidenceBeneficiaryCredit  Evidence = "BENEFICIARY_CREDIT"
	EvidenceAuthorityReceipt   Evidence = "AUTHORITY_RECEIPT"
	EvidenceBankStatement      Evidence = "BANK_STATEMENT"
	EvidenceProviderSettled    Evidence = "PROVIDER_SETTLED"
	EvidenceManual             Evidence = "MANUAL_EVIDENCE"
	EvidenceNone               Evidence = "NONE"
)

// Valid reports whether the evidence kind is one of §18's.
func (e Evidence) Valid() bool {
	switch e {
	case EvidenceProviderAcceptance, EvidenceProcessingRef, EvidenceAccountDebit, EvidenceBeneficiaryCredit,
		EvidenceAuthorityReceipt, EvidenceBankStatement, EvidenceProviderSettled, EvidenceManual, EvidenceNone:
		return true
	}
	return false
}

// Settles reports whether the evidence proves completion. Acceptance, a
// processing reference and a payer debit do not: a debited payment can
// still be returned (ZTAX-REM-REQ-0043, -0044, -0075).
func (e Evidence) Settles() bool {
	switch e {
	case EvidenceBeneficiaryCredit, EvidenceAuthorityReceipt, EvidenceBankStatement, EvidenceProviderSettled, EvidenceManual:
		return true
	}
	return false
}

// Resolution settles an uncertain payment (ZTAX-REM-001 §19).
type Resolution string

// The resolutions.
const (
	ResolutionSettled   Resolution = "CONFIRMED_SETTLED"
	ResolutionNotSent   Resolution = "CONFIRMED_NOT_SENT"
	ResolutionRejected  Resolution = "CONFIRMED_REJECTED"
	ResolutionReturned  Resolution = "RETURNED"
	ResolutionDuplicate Resolution = "DUPLICATE_SUSPECTED"
	ResolutionManual    Resolution = "MANUAL_INVESTIGATION"
)

// Outcome is the status a resolution moves an uncertain payment to.
// CONFIRMED_NOT_SENT goes back to APPROVAL_REQUIRED, not APPROVED: a second
// payment after uncertainty needs an explicit new approval
// (ZTAX-REM-001 §19).
func (r Resolution) Outcome() (Status, bool) {
	switch r {
	case ResolutionSettled:
		return StatusSettled, true
	case ResolutionNotSent:
		return StatusApprovalRequired, true
	case ResolutionRejected:
		return StatusRejected, true
	case ResolutionReturned:
		return StatusReturned, true
	case ResolutionDuplicate, ResolutionManual:
		return StatusUncertain, true
	}
	return "", false
}

// StepKind is what a step records.
type StepKind string

// The step kinds.
const (
	StepCreated   StepKind = "CREATED"
	StepApproved  StepKind = "APPROVED"
	StepSubmitted StepKind = "SUBMITTED"
	StepStatus    StepKind = "PROVIDER_STATUS"
	StepResolved  StepKind = "UNCERTAIN_RESOLVED"
	StepReturned  StepKind = "RETURNED"
	StepCancelled StepKind = "CANCELLED"
)

// Step is one act on a remittance.
type Step struct {
	Seq    int
	Kind   StepKind
	Status Status

	// InstructionHash is the hash an approval or submission bound.
	InstructionHash canonical.Digest
	Submission      id.SubmissionAttemptID
	Evidence        Evidence
	Resolution      Resolution
	// ProviderRef is the provider's or authority's own reference.
	ProviderRef string
	// Raw is the provider's response as received, or the operator's account
	// of it.
	Raw  string
	Note string

	RecordedAt time.Time
	RecordedBy id.UserID
}

// Remittance is one immutable payment instruction, folded from its steps.
type Remittance struct {
	ID       id.RemittanceID
	TenantID id.TenantID

	// The obligation it pays, as it stood when the instruction was made.
	Obligation    id.ObligationID
	ObligationKey string
	LegalEntity   id.LegalEntityID
	Authority     string
	PeriodStart   time.Time
	PeriodEnd     time.Time
	// PaymentDue is the obligation's legal due date, end of day in its
	// calendar.
	PaymentDue time.Time
	Timezone   string

	Amount      fiscal.Money
	Beneficiary id.BeneficiaryID
	// FundingRef names the customer's own account, by reference.
	FundingRef       string
	PaymentReference string
	ExecutionDate    time.Time
	Mode             Mode
	// Supersedes is the remittance this one replaces after it was cancelled,
	// rejected or returned (ZTAX-REM-REQ-0008, -0041).
	Supersedes id.RemittanceID

	InstructionHash canonical.Digest

	CreatedAt time.Time
	CreatedBy id.UserID

	Status  Status
	History []Step
}

// Instruction is what the hash covers: every field a payment executes on.
func (r Remittance) Instruction() canonical.Value {
	sup := canonical.Absent()
	if !r.Supersedes.IsZero() {
		sup = canonical.String(r.Supersedes.String())
	}
	return canonical.Object(
		canonical.F("remittanceId", canonical.String(r.ID.String())),
		canonical.F("obligationId", canonical.String(r.Obligation.String())),
		canonical.F("legalEntityId", canonical.String(r.LegalEntity.String())),
		canonical.F("authority", canonical.String(r.Authority)),
		canonical.F("amount", canonical.Money(r.Amount)),
		canonical.F("currency", canonical.String(string(r.Amount.Currency()))),
		canonical.F("beneficiaryId", canonical.String(r.Beneficiary.String())),
		canonical.F("fundingRef", canonical.String(r.FundingRef)),
		canonical.F("paymentReference", canonical.String(r.PaymentReference)),
		canonical.F("executionDate", canonical.String(r.ExecutionDate.Format(time.DateOnly))),
		canonical.F("mode", canonical.String(string(r.Mode))),
		canonical.F("supersedes", sup),
	)
}

// Seal computes the instruction hash. It is called once, when the
// instruction is made; nothing afterwards changes what it covers.
func (r *Remittance) Seal() error {
	d, err := canonical.Sum(r.Instruction())
	if err != nil {
		return err
	}
	r.InstructionHash = d
	return nil
}

// Validate refuses an instruction that cannot be made.
func (r Remittance) Validate() error {
	switch {
	case r.Obligation.IsZero() || r.ObligationKey == "":
		return fmt.Errorf("remittance: instruction references no obligation")
	case r.LegalEntity.IsZero():
		return fmt.Errorf("remittance: instruction names no payer legal entity")
	case r.Beneficiary.IsZero():
		return fmt.Errorf("remittance: instruction names no beneficiary version")
	case !IsSecureRef(r.FundingRef):
		return fmt.Errorf("remittance: the funding account is named by a vault or token reference to the customer's own account")
	case strings.TrimSpace(r.PaymentReference) == "":
		return fmt.Errorf("remittance: instruction carries the authority's payment reference")
	case r.ExecutionDate.IsZero():
		return fmt.Errorf("remittance: instruction names its execution date")
	case !r.Mode.Valid():
		return fmt.Errorf("remittance: operating mode %q is not one this service knows", r.Mode)
	case r.PaymentDue.IsZero() || r.Timezone == "":
		return fmt.Errorf("remittance: instruction has no payment due date")
	}
	if r.Amount.Sign() <= 0 {
		return fmt.Errorf("remittance: a payment is of a positive amount")
	}
	return nil
}

// transitions is the payment state machine (ZTAX-REM-001 §10).
var transitions = map[Status][]Status{
	StatusApprovalRequired: {StatusApproved, StatusCancelled},
	StatusApproved:         {StatusSubmitted, StatusCancelled},
	StatusSubmitted:        {StatusAccepted, StatusProcessing, StatusSettled, StatusRejected, StatusUncertain},
	StatusAccepted:         {StatusProcessing, StatusSettled, StatusRejected, StatusUncertain},
	StatusProcessing:       {StatusProcessing, StatusSettled, StatusRejected, StatusUncertain},
	StatusUncertain:        {StatusUncertain, StatusSettled, StatusApprovalRequired, StatusRejected, StatusReturned},
	StatusSettled:          {StatusReturned},
	StatusRejected:         nil,
	StatusReturned:         nil,
	StatusCancelled:        nil,
}

// CanTransition reports whether a move is permitted.
func CanTransition(from, to Status) bool { return slices.Contains(transitions[from], to) }

// Fold applies a remittance's steps in order.
func Fold(r Remittance, steps []Step) (Remittance, error) {
	if len(steps) == 0 || steps[0].Kind != StepCreated || steps[0].Seq != 1 {
		return Remittance{}, fmt.Errorf("remittance: a payment's history starts with its CREATED step")
	}
	r.Status = steps[0].Status
	for i, s := range steps[1:] {
		if s.Seq != i+2 || s.Kind == StepCreated {
			return Remittance{}, fmt.Errorf("remittance: step %d is out of sequence", s.Seq)
		}
		if !CanTransition(r.Status, s.Status) {
			return Remittance{}, fmt.Errorf("remittance: step %d moves %s to %s", s.Seq, r.Status, s.Status)
		}
		r.Status = s.Status
	}
	r.History = steps
	return r, nil
}

func (r Remittance) next(kind StepKind, to Status, by id.UserID, at time.Time) (Step, error) {
	if !CanTransition(r.Status, to) {
		return Step{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A payment that is %s cannot move to %s.", r.Status, to))
	}
	if by.IsZero() {
		return Step{}, errs.New(errs.CategoryPolicy, errs.ReasonForbidden, "Every payment step is recorded against a person.")
	}
	return Step{Seq: len(r.History) + 1, Kind: kind, Status: to, RecordedAt: at.UTC(), RecordedBy: by}, nil
}

// Approval is the approval in force: the latest APPROVED step, unless an
// uncertain resolution has since sent the payment back for approval.
func (r Remittance) Approval() *Step {
	for i := len(r.History) - 1; i >= 0; i-- {
		s := r.History[i]
		if s.Kind == StepApproved {
			return &r.History[i]
		}
		if s.Kind == StepResolved && s.Status == StatusApprovalRequired {
			return nil
		}
	}
	return nil
}

// Submissions are the submission attempts, oldest first.
func (r Remittance) Submissions() []Step {
	var out []Step
	for _, s := range r.History {
		if s.Kind == StepSubmitted {
			out = append(out, s)
		}
	}
	return out
}

// Approve binds the exact instruction hash (ZTAX-REM-REQ-0026). The maker
// does not approve their own instruction (ZTAX-REM-REQ-0024).
func (r Remittance) Approve(hash canonical.Digest, note string, by id.UserID, at time.Time) (Step, error) {
	if r.Status != StatusApprovalRequired {
		return Step{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only a payment awaiting approval is approved; this one is %s.", r.Status))
	}
	if hash != r.InstructionHash {
		return Step{}, errs.New(errs.CategoryConflict, ReasonInstructionChanged,
			"The hash approved is not this instruction's (ZTAX-REM-REQ-0026).")
	}
	if by == r.CreatedBy {
		return Step{}, errs.New(errs.CategoryPolicy, ReasonMakerChecker,
			"The person who made this instruction cannot approve it (ZTAX-REM-REQ-0024).")
	}
	s, err := r.next(StepApproved, StatusApproved, by, at)
	if err != nil {
		return Step{}, err
	}
	s.InstructionHash, s.Note = r.InstructionHash, note
	return s, nil
}

// Submit records the instruction leaving for its executor, under its own
// attempt identity and the approved hash (ZTAX-REM-REQ-0055).
func (r Remittance) Submit(attempt id.SubmissionAttemptID, note string, by id.UserID, at time.Time) (Step, error) {
	if r.Status != StatusApproved || r.Approval() == nil {
		return Step{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only an approved payment is submitted; this one is %s.", r.Status))
	}
	if attempt.IsZero() {
		return Step{}, errs.New(errs.CategoryInternal, errs.ReasonInternal, "A submission is recorded with its identifier.")
	}
	s, err := r.next(StepSubmitted, StatusSubmitted, by, at)
	if err != nil {
		return Step{}, err
	}
	s.InstructionHash, s.Submission, s.Note = r.InstructionHash, attempt, note
	return s, nil
}

// RecordStatus records what the provider reported. SETTLED needs settlement
// evidence and the provider's reference; an answer nobody can read is
// UNCERTAIN_PAYMENT (ZTAX-REM-REQ-0040, -0043, -0044).
func (r Remittance) RecordStatus(to Status, evidence Evidence, providerRef, raw, note string, by id.UserID, at time.Time) (Step, error) {
	switch to {
	case StatusAccepted, StatusProcessing, StatusSettled, StatusRejected, StatusUncertain:
	default:
		return Step{}, errs.Invalid("status", errs.ReasonInvalidValue,
			fmt.Sprintf("%q is not a status a provider reports; ACCEPTED_BY_PROVIDER, PROCESSING, SETTLED, REJECTED or UNCERTAIN_PAYMENT.", to))
	}
	if !evidence.Valid() {
		return Step{}, errs.Invalid("evidence", errs.ReasonInvalidValue, fmt.Sprintf("%q is not an evidence kind.", evidence))
	}
	if r.Status == StatusUncertain {
		return Step{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			"An uncertain payment is resolved with what was established, not with another provider status.")
	}
	if to == StatusSettled {
		if !evidence.Settles() {
			return Step{}, errs.New(errs.CategoryConflict, ReasonPaymentBlocked,
				fmt.Sprintf("%s is not settlement evidence; a debited or accepted payment can still be returned (ZTAX-REM-REQ-0043).", evidence))
		}
		if strings.TrimSpace(providerRef) == "" {
			return Step{}, errs.Invalid("providerReference", errs.ReasonMissingField, "Settlement names the reference that evidences it.")
		}
	}
	if strings.TrimSpace(raw) == "" {
		return Step{}, errs.Invalid("raw", errs.ReasonMissingField, "The provider's response is recorded as received.")
	}
	s, err := r.next(StepStatus, to, by, at)
	if err != nil {
		return Step{}, err
	}
	s.InstructionHash, s.Evidence, s.ProviderRef, s.Raw, s.Note = r.InstructionHash, evidence, strings.TrimSpace(providerRef), raw, note
	if subs := r.Submissions(); len(subs) > 0 {
		s.Submission = subs[len(subs)-1].Submission
	}
	return s, nil
}

// Resolve settles an uncertain payment with what was established. Every
// attempt stays in the history (ZTAX-REM-001 §19).
func (r Remittance) Resolve(res Resolution, evidence Evidence, providerRef, raw string, by id.UserID, at time.Time) (Step, error) {
	to, ok := res.Outcome()
	if !ok {
		return Step{}, errs.Invalid("resolution", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a resolution.", res))
	}
	if r.Status != StatusUncertain {
		return Step{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only an uncertain payment is resolved; this one is %s.", r.Status))
	}
	if strings.TrimSpace(raw) == "" {
		return Step{}, errs.Invalid("raw", errs.ReasonMissingField, "A resolution records what was established, and how.")
	}
	if res == ResolutionSettled && (!evidence.Settles() || strings.TrimSpace(providerRef) == "") {
		return Step{}, errs.New(errs.CategoryConflict, ReasonPaymentBlocked,
			"Confirming settlement names settlement evidence and its reference (ZTAX-REM-REQ-0043).")
	}
	s, err := r.next(StepResolved, to, by, at)
	if err != nil {
		return Step{}, err
	}
	s.Resolution, s.Evidence, s.ProviderRef, s.Raw = res, evidence, strings.TrimSpace(providerRef), raw
	return s, nil
}

// Return records value coming back after settlement. The settled step stays;
// the return is a step after it (ZTAX-REM-REQ-0041).
func (r Remittance) Return(providerRef, raw string, by id.UserID, at time.Time) (Step, error) {
	if strings.TrimSpace(raw) == "" {
		return Step{}, errs.Invalid("raw", errs.ReasonMissingField, "A return records the provider's notice as received.")
	}
	s, err := r.next(StepReturned, StatusReturned, by, at)
	if err != nil {
		return Step{}, err
	}
	s.ProviderRef, s.Raw = strings.TrimSpace(providerRef), raw
	return s, nil
}

// Cancel withdraws an instruction that has not been sent.
func (r Remittance) Cancel(reason string, by id.UserID, at time.Time) (Step, error) {
	if strings.TrimSpace(reason) == "" {
		return Step{}, errs.Invalid("reason", errs.ReasonMissingField, "A cancellation gives a reason.")
	}
	if r.Status.Sent() {
		return Step{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			"A payment that has been sent is recalled through its provider, not cancelled here.")
	}
	s, err := r.next(StepCancelled, StatusCancelled, by, at)
	if err != nil {
		return Step{}, err
	}
	s.Note = reason
	return s, nil
}

// Overdue reports whether the payment due date has passed unpaid.
func (r Remittance) Overdue(asOf time.Time) bool {
	switch r.Status {
	case StatusSettled, StatusCancelled, StatusReturned:
		return false
	}
	return asOf.After(r.PaymentDue)
}

// ReferenceValid reports whether a payment reference satisfies the
// beneficiary's rule.
func ReferenceValid(rule, reference string) bool {
	if rule == "" {
		return strings.TrimSpace(reference) != ""
	}
	re, err := regexp.Compile("^(?:" + rule + ")$")
	return err == nil && re.MatchString(reference)
}
