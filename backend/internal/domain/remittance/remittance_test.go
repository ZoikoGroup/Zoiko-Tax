package remittance_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/remittance"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

// Lane M's remittance domain against ZTAX-REM-001's register. Test names
// carry the requirement id each verifies, so docs/requirements.yaml can point
// at it.

var (
	tenant   = id.NewTenantID(uuid.MustParse("00000000-0000-7000-8000-000000000001"))
	entity   = id.NewLegalEntityID(uuid.MustParse("00000000-0000-7000-8000-000000000002"))
	maker    = id.NewUserID(uuid.MustParse("00000000-0000-7000-8000-000000000004"))
	checker  = id.NewUserID(uuid.MustParse("00000000-0000-7000-8000-000000000005"))
	attempt  = id.NewSubmissionAttemptID(uuid.MustParse("00000000-0000-7000-8000-000000000007"))
	when     = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	dueDate  = time.Date(2026, 10, 20, 23, 59, 59, 999999000, time.UTC)
	execDate = time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
)

func reason(t *testing.T, err error, want errs.ReasonCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("succeeded; want %s", want)
	}
	if got := errs.ReasonOf(err); got != want {
		t.Fatalf("reason %s, want %s (%v)", got, want, err)
	}
}

func payee(t *testing.T) remittance.Beneficiary {
	t.Helper()
	b := remittance.Beneficiary{
		ID: id.NewBeneficiaryID(uuid.MustParse("00000000-0000-7000-8000-000000000020")), TenantID: tenant,
		Country: "DE", Authority: "DE-ELSTER", LegalName: "Finanzamt Musterstadt", Rail: remittance.RailSEPA,
		AccountRef: "vault://payees/de/finanzamt", ReferenceRule: "[0-9]{3}/[0-9]{3}/[0-9]{5}", Currencies: []string{"EUR"},
		Source: "official instructions 2026", CreatedAt: when, CreatedBy: maker,
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	out, err := remittance.FoldBeneficiary(b, []remittance.BeneficiaryEvent{{Seq: 1, Kind: remittance.BeneficiaryCreated, RecordedAt: when, RecordedBy: maker}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func verified(t *testing.T) remittance.Beneficiary {
	t.Helper()
	b := payee(t)
	e, err := b.Verify("called the authority", checker, when)
	if err != nil {
		t.Fatal(err)
	}
	out, err := remittance.FoldBeneficiary(b, append(append([]remittance.BeneficiaryEvent{}, b.History...), e))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func instruction(t *testing.T) remittance.Remittance {
	t.Helper()
	r := remittance.Remittance{
		ID: id.NewRemittanceID(uuid.MustParse("00000000-0000-7000-8000-000000000030")), TenantID: tenant,
		Obligation:    id.NewObligationID(uuid.MustParse("00000000-0000-7000-8000-000000000011")),
		ObligationKey: "xa-vat-return/le/2026-07-01", LegalEntity: entity, Authority: "authority:xa-revenue",
		PeriodStart: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), PeriodEnd: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
		PaymentDue: dueDate, Timezone: "UTC", Amount: fiscaltest.Money(t, "21.00", "EUR"),
		Beneficiary: id.NewBeneficiaryID(uuid.MustParse("00000000-0000-7000-8000-000000000020")),
		FundingRef:  "vault://funding/acme/eur", PaymentReference: "123/456/78901", ExecutionDate: execDate,
		Mode: remittance.ModeCustomerExecutes, CreatedAt: when, CreatedBy: maker,
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := r.Seal(); err != nil {
		t.Fatal(err)
	}
	out, err := remittance.Fold(r, []remittance.Step{{Seq: 1, Kind: remittance.StepCreated, Status: remittance.StatusApprovalRequired,
		RecordedAt: when, RecordedBy: maker}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func apply(t *testing.T, r remittance.Remittance, s remittance.Step, err error) remittance.Remittance {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	out, err := remittance.Fold(r, append(append([]remittance.Step{}, r.History...), s))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func submitted(t *testing.T) remittance.Remittance {
	t.Helper()
	r := instruction(t)
	s, err := r.Approve(r.InstructionHash, "", checker, when)
	r = apply(t, r, s, err)
	s, err = r.Submit(attempt, "", maker, when)
	return apply(t, r, s, err)
}

func TestREMREQ0009And0011AccountsAreReferencesNotNumbers(t *testing.T) {
	b := payee(t)
	for _, raw := range []string{"DE89370400440532013000", "iban:DE89370400440532013000", ""} {
		b.AccountRef = raw
		if err := b.Validate(); err == nil {
			t.Fatalf("payee account %q accepted; only a vault or token reference is", raw)
		}
	}
	r := instruction(t)
	r.FundingRef = "DE89370400440532013000"
	if err := r.Validate(); err == nil {
		t.Fatal("a funding account number was accepted; it is the customer's account, by reference")
	}
}

func TestREMREQ0014And0015PayeesAreVerifiedBySomeoneElseBeforePayment(t *testing.T) {
	b := payee(t)
	if ok, _ := b.Payable("EUR"); ok {
		t.Fatal("an unverified payee is payable")
	}
	_, err := b.Verify("I checked", maker, when)
	reason(t, err, remittance.ReasonMakerChecker)
	v := verified(t)
	if ok, _ := v.Payable("EUR"); !ok {
		t.Fatal("a verified payee is not payable")
	}
	if ok, _ := v.Payable("USD"); ok {
		t.Fatal("a payee is payable in a currency it does not accept")
	}
}

func TestREMREQ0012AReplacementIsChangePendingUntilVerified(t *testing.T) {
	b := payee(t)
	b.Supersedes = id.NewBeneficiaryID(uuid.MustParse("00000000-0000-7000-8000-000000000021"))
	out, err := remittance.FoldBeneficiary(b, b.History)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != remittance.BeneficiaryChangePending {
		t.Fatalf("state %s, want CHANGE_PENDING", out.State)
	}
	if ok, _ := out.Payable("EUR"); ok {
		t.Fatal("a CHANGE_PENDING payee is payable")
	}
}

func TestREMREQ0024And0026ApprovalBindsTheExactHashAndAnotherPerson(t *testing.T) {
	r := instruction(t)
	_, err := r.Approve(r.InstructionHash, "", maker, when)
	reason(t, err, remittance.ReasonMakerChecker)

	other := instruction(t)
	other.PaymentReference = "999/999/99999"
	if err := other.Seal(); err != nil {
		t.Fatal(err)
	}
	if other.InstructionHash == r.InstructionHash {
		t.Fatal("two different instructions hashed alike")
	}
	_, err = r.Approve(other.InstructionHash, "", checker, when)
	reason(t, err, remittance.ReasonInstructionChanged)

	s, err := r.Approve(r.InstructionHash, "", checker, when)
	r = apply(t, r, s, err)
	if a := r.Approval(); a == nil || a.InstructionHash != r.InstructionHash {
		t.Fatal("the approval does not bind the instruction hash")
	}
}

func TestREMREQ0036TheHashCoversEveryExecutedField(t *testing.T) {
	base := instruction(t)
	for name, change := range map[string]func(*remittance.Remittance){
		"amount":    func(r *remittance.Remittance) { r.Amount = fiscaltest.Money(t, "21.01", "EUR") },
		"payee":     func(r *remittance.Remittance) { r.Beneficiary = id.NewBeneficiaryID(uuid.New()) },
		"funding":   func(r *remittance.Remittance) { r.FundingRef = "vault://funding/other" },
		"reference": func(r *remittance.Remittance) { r.PaymentReference = "000/000/00000" },
		"date":      func(r *remittance.Remittance) { r.ExecutionDate = execDate.AddDate(0, 0, 1) },
		"mode":      func(r *remittance.Remittance) { r.Mode = remittance.ModeBankAPIInitiation },
	} {
		r := base
		change(&r)
		if err := r.Seal(); err != nil {
			t.Fatal(err)
		}
		if r.InstructionHash == base.InstructionHash {
			t.Fatalf("changing the %s did not change the instruction hash", name)
		}
	}
}

func TestREMREQ0037To0039And0044SubmittedAcceptedAndProcessingAreNotSettled(t *testing.T) {
	r := submitted(t)
	if r.Status != remittance.StatusSubmitted {
		t.Fatalf("status %s", r.Status)
	}
	s, err := r.RecordStatus(remittance.StatusAccepted, remittance.EvidenceProviderAcceptance, "PSP-1", "200 OK id=PSP-1", "", maker, when)
	r = apply(t, r, s, err)
	s, err = r.RecordStatus(remittance.StatusProcessing, remittance.EvidenceProcessingRef, "NET-1", "in process", "", maker, when)
	r = apply(t, r, s, err)
	if r.Status == remittance.StatusSettled {
		t.Fatal("acceptance or processing settled the payment")
	}
}

func TestREMREQ0043And0075OnlySettlementEvidenceSettles(t *testing.T) {
	r := submitted(t)
	for _, e := range []remittance.Evidence{remittance.EvidenceProviderAcceptance, remittance.EvidenceProcessingRef, remittance.EvidenceAccountDebit, remittance.EvidenceNone} {
		_, err := r.RecordStatus(remittance.StatusSettled, e, "REF", "raw", "", maker, when)
		reason(t, err, remittance.ReasonPaymentBlocked)
	}
	if _, err := r.RecordStatus(remittance.StatusSettled, remittance.EvidenceBankStatement, "", "raw", "", maker, when); errs.CategoryOf(err) != errs.CategoryValidation {
		t.Fatalf("settlement with no reference: %v", err)
	}
	s, err := r.RecordStatus(remittance.StatusSettled, remittance.EvidenceBankStatement, "STMT-1", "statement line", "", maker, when)
	r = apply(t, r, s, err)
	if r.Status != remittance.StatusSettled {
		t.Fatalf("status %s", r.Status)
	}
}

func TestREMREQ0040UncertainPaymentNeedsAPersonAndANewApproval(t *testing.T) {
	r := submitted(t)
	s, err := r.RecordStatus(remittance.StatusUncertain, remittance.EvidenceNone, "", "timeout after send", "", maker, when)
	r = apply(t, r, s, err)
	if _, err := r.Submit(id.NewSubmissionAttemptID(uuid.New()), "", maker, when); err == nil {
		t.Fatal("an uncertain payment was submitted again")
	}
	if _, err := r.RecordStatus(remittance.StatusSettled, remittance.EvidenceBankStatement, "S", "raw", "", maker, when); err == nil {
		t.Fatal("an uncertain payment was moved by a provider status rather than a resolution")
	}
	s, err = r.Resolve(remittance.ResolutionNotSent, remittance.EvidenceNone, "", "bank shows nothing left the account", maker, when)
	r = apply(t, r, s, err)
	if r.Status != remittance.StatusApprovalRequired || r.Approval() != nil {
		t.Fatal("a payment confirmed not sent went straight back to approved")
	}
	if len(r.Submissions()) != 1 {
		t.Fatal("the uncertain attempt left the history")
	}
}

func TestREMREQ0041AReturnKeepsTheSettlement(t *testing.T) {
	r := submitted(t)
	s, err := r.RecordStatus(remittance.StatusSettled, remittance.EvidenceAuthorityReceipt, "R-1", "receipt", "", maker, when)
	r = apply(t, r, s, err)
	s, err = r.Return("RET-1", "account closed", maker, when)
	r = apply(t, r, s, err)
	if r.Status != remittance.StatusReturned || r.History[len(r.History)-2].Status != remittance.StatusSettled {
		t.Fatal("the return did not follow the settlement it reverses")
	}
}

func TestREMREQ0033ASentInstructionIsNotCancelled(t *testing.T) {
	if _, err := submitted(t).Cancel("changed our mind", maker, when); err == nil {
		t.Fatal("a sent payment was cancelled")
	}
	r := instruction(t)
	s, err := r.Cancel("liability amended", maker, when)
	r = apply(t, r, s, err)
	if r.Status != remittance.StatusCancelled {
		t.Fatalf("status %s", r.Status)
	}
}

func TestREMREQ0022TheReferenceFollowsTheAuthoritysFormat(t *testing.T) {
	rule := verified(t).ReferenceRule
	if !remittance.ReferenceValid(rule, "123/456/78901") || remittance.ReferenceValid(rule, "INV-1") {
		t.Fatal("the payment reference rule is not applied")
	}
}
