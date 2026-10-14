package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/remittance"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Remittance and funds control (ZTAX-REM-001, W4 lane M). The rules are
// internal/domain/remittance's; this is where a payment meets the obligation
// it pays, the payee version it pays to, and the store.
//
// The posture is fixed: NO_CUSTODY. ZoikoTax prepares, authorizes and
// records instructions to the customer's own regulated provider, and holds
// nothing (ZTAX-REM-REQ-0001). A cell that wanted otherwise would need a
// separate legal and architectural review, not a setting.
//
// Every write is a person's act, so maker-checker is a comparison of people
// and models never approve (ZTAX-REM-REQ-0029).

// FundsPosture is the funds-control posture every remittance is made under.
const FundsPosture = "NO_CUSTODY"

// RemittanceService runs payee master data and payment instructions.
type RemittanceService struct {
	store       port.RemittanceRepository
	obligations port.ObligationRepository
	payments    *DeterminationService
	tx          port.TxManager
	clock       clock.Clock
	ids         idgen.Generator
}

// NewRemittanceService wires the service. payments moves obligations on
// payment evidence.
func NewRemittanceService(store port.RemittanceRepository, obligations port.ObligationRepository, payments *DeterminationService,
	tx port.TxManager, clk clock.Clock, ids idgen.Generator) *RemittanceService {
	return &RemittanceService{store: store, obligations: obligations, payments: payments, tx: tx, clock: clk, ids: ids}
}

func (s *RemittanceService) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }

// The roles. Payee master data is maintained by an administrator or an
// operator and verified by someone else with a fiscal role; payments are
// made and submitted by operators and approved by an operator or analyst
// who did not make them.
var (
	remittanceReaders   = []security.Role{security.RoleAdmin, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor}
	beneficiaryMakers   = []security.Role{security.RoleAdmin, security.RoleOperator}
	beneficiaryCheckers = []security.Role{security.RoleAdmin, security.RoleOperator, security.RoleAnalyst}
	remittanceOperators = []security.Role{security.RoleOperator}
	remittanceApprovers = []security.Role{security.RoleOperator, security.RoleAnalyst}
)

// ---------------------------------------------------------------------------
// beneficiaries
// ---------------------------------------------------------------------------

// BeneficiaryInput is a payee version to record.
type BeneficiaryInput struct {
	Supersedes    id.BeneficiaryID
	Country       string
	Authority     string
	LegalName     string
	Rail          remittance.Rail
	AccountRef    string
	ReferenceRule string
	Currencies    []string
	Source        string
}

// CreateBeneficiary records a payee version, unverified. A version that
// supersedes another is CHANGE_PENDING until verified, and the version it
// replaces stays usable until then.
func (s *RemittanceService) CreateBeneficiary(ctx context.Context, in BeneficiaryInput) (remittance.Beneficiary, error) {
	sc, err := requirePerson(ctx, beneficiaryMakers...)
	if err != nil {
		return remittance.Beneficiary{}, err
	}
	bid, err := idgen.BeneficiaryID(s.ids)
	if err != nil {
		return remittance.Beneficiary{}, internal(err, "The beneficiary could not be recorded.")
	}
	now := s.now()
	currencies := make([]string, 0, len(in.Currencies))
	for _, c := range in.Currencies {
		currencies = append(currencies, strings.ToUpper(strings.TrimSpace(c)))
	}
	b := remittance.Beneficiary{
		ID: bid, TenantID: sc.Tenant(), Supersedes: in.Supersedes, Country: strings.ToUpper(strings.TrimSpace(in.Country)),
		Authority: strings.TrimSpace(in.Authority), LegalName: strings.TrimSpace(in.LegalName), Rail: in.Rail,
		AccountRef: strings.TrimSpace(in.AccountRef), ReferenceRule: strings.TrimSpace(in.ReferenceRule),
		Currencies: slices.Compact(slices.Sorted(slices.Values(currencies))), Source: strings.TrimSpace(in.Source),
		CreatedAt: now, CreatedBy: sc.Subject(),
	}
	if err := b.Validate(); err != nil {
		return remittance.Beneficiary{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue, strings.TrimPrefix(err.Error(), "remittance: ")+".")
	}
	var out remittance.Beneficiary
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "beneficiaries"); err != nil {
			return err
		}
		if !b.Supersedes.IsZero() {
			old, err := s.store.Beneficiary(ctx, b.Supersedes)
			if err != nil {
				return err
			}
			if old.State == remittance.BeneficiarySuperseded {
				return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
					"That version has already been replaced; change the current version.")
			}
			if old.Country != b.Country || old.Authority != b.Authority {
				return errs.Invalid("supersedes", errs.ReasonInvalidValue, "A new version is of the same authority's payee details.")
			}
		}
		if err := s.store.CreateBeneficiary(ctx, b, remittance.BeneficiaryEvent{Seq: 1, Kind: remittance.BeneficiaryCreated,
			RecordedAt: now, RecordedBy: sc.Subject()}); err != nil {
			return err
		}
		out, err = s.store.Beneficiary(ctx, bid)
		return err
	})
	return out, err
}

// VerifyBeneficiary records an independent verification. Verifying a
// replacement supersedes the version it replaces in the same act, so there is
// never a moment with two payable versions of one payee.
func (s *RemittanceService) VerifyBeneficiary(ctx context.Context, beneficiaryID id.BeneficiaryID, evidence string) (remittance.Beneficiary, error) {
	sc, err := requirePerson(ctx, beneficiaryCheckers...)
	if err != nil {
		return remittance.Beneficiary{}, err
	}
	var out remittance.Beneficiary
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "beneficiaries"); err != nil {
			return err
		}
		b, err := s.store.Beneficiary(ctx, beneficiaryID)
		if err != nil {
			return err
		}
		now := s.now()
		e, err := b.Verify(strings.TrimSpace(evidence), sc.Subject(), now)
		if err != nil {
			return err
		}
		if err := s.store.AppendBeneficiaryEvent(ctx, b.ID, e); err != nil {
			return err
		}
		if !b.Supersedes.IsZero() {
			old, err := s.store.Beneficiary(ctx, b.Supersedes)
			if err != nil {
				return err
			}
			if old.State != remittance.BeneficiarySuperseded {
				if err := s.store.AppendBeneficiaryEvent(ctx, old.ID, remittance.BeneficiaryEvent{Seq: len(old.History) + 1,
					Kind: remittance.BeneficiarySupersede, Evidence: "replaced by " + b.ID.String(), RecordedAt: now, RecordedBy: sc.Subject()}); err != nil {
					return err
				}
			}
		}
		out, err = s.store.Beneficiary(ctx, b.ID)
		return err
	})
	return out, err
}

// SuspendBeneficiary stops payments to a payee version, with its reason.
func (s *RemittanceService) SuspendBeneficiary(ctx context.Context, beneficiaryID id.BeneficiaryID, reason string) (remittance.Beneficiary, error) {
	sc, err := requirePerson(ctx, beneficiaryCheckers...)
	if err != nil {
		return remittance.Beneficiary{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return remittance.Beneficiary{}, errs.Invalid("reason", errs.ReasonMissingField, "A suspension gives a reason.")
	}
	var out remittance.Beneficiary
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "beneficiaries"); err != nil {
			return err
		}
		b, err := s.store.Beneficiary(ctx, beneficiaryID)
		if err != nil {
			return err
		}
		if b.State == remittance.BeneficiarySuspended || b.State == remittance.BeneficiarySuperseded {
			return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid, fmt.Sprintf("The beneficiary is already %s.", b.State))
		}
		if err := s.store.AppendBeneficiaryEvent(ctx, b.ID, remittance.BeneficiaryEvent{Seq: len(b.History) + 1,
			Kind: remittance.BeneficiarySuspend, Evidence: strings.TrimSpace(reason), RecordedAt: s.now(), RecordedBy: sc.Subject()}); err != nil {
			return err
		}
		out, err = s.store.Beneficiary(ctx, b.ID)
		return err
	})
	return out, err
}

// Beneficiaries lists the tenant's payee versions, newest first.
func (s *RemittanceService) Beneficiaries(ctx context.Context) ([]remittance.Beneficiary, error) {
	if _, err := requireRoleOrSystem(ctx, remittanceReaders...); err != nil {
		return nil, err
	}
	return s.store.Beneficiaries(ctx)
}

// ---------------------------------------------------------------------------
// payments
// ---------------------------------------------------------------------------

// RemittanceView is a payment as of now: the payee version it pins, the
// obligation's current status, readiness and whether it is overdue.
type RemittanceView struct {
	remittance.Remittance
	Beneficiary      remittance.Beneficiary
	ObligationStatus obligation.Status
	Readiness        []PackageReadiness
	// Ready is every readiness dimension passing; unknown is not ready
	// (ZTAX-REM-REQ-0023).
	Ready   bool
	Overdue bool
}

// RemittanceInput is a payment instruction to make.
type RemittanceInput struct {
	Obligation       id.ObligationID
	Beneficiary      id.BeneficiaryID
	FundingRef       string
	PaymentReference string
	ExecutionDate    time.Time
	Mode             remittance.Mode
}

// CreateRemittance makes the payment instruction for an accepted
// obligation's liability, and records the obligation as payment-due. It is
// idempotent for the obligation: while a live instruction exists, asking
// again returns it. Only after one was cancelled, rejected or returned is a
// new one made, linked to it (ZTAX-REM-REQ-0007, -0008, -0061).
func (s *RemittanceService) CreateRemittance(ctx context.Context, in RemittanceInput) (RemittanceView, bool, error) {
	sc, err := requirePerson(ctx, remittanceOperators...)
	if err != nil {
		return RemittanceView{}, false, err
	}
	var (
		out     remittance.Remittance
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		o, err := s.obligations.ByID(ctx, in.Obligation)
		if err != nil {
			return err
		}
		if err := s.store.Lock(ctx, o.BusinessKey); err != nil {
			return err
		}
		chain, err := s.store.ByObligation(ctx, o.BusinessKey)
		if err != nil {
			return err
		}
		var supersedes id.RemittanceID
		if len(chain) > 0 {
			last := chain[len(chain)-1]
			if !last.Status.Terminal() {
				out = last
				return nil
			}
			supersedes = last.ID
		}
		cur, err := s.obligations.Current(ctx, o.BusinessKey)
		if err != nil {
			return err
		}
		if cur.Status != obligation.StatusAccepted && cur.Status != obligation.StatusPaymentDue {
			return errs.New(errs.CategoryConflict, remittance.ReasonPaymentBlocked,
				fmt.Sprintf("The obligation is %s. A payment is made for a return the authority accepted (ZTAX-REM-REQ-0005).", cur.Status))
		}
		if !cur.Duty.Monetary() {
			return errs.New(errs.CategoryConflict, remittance.ReasonPaymentBlocked, "A non-monetary obligation is never paid.")
		}
		amount, err := s.liability(ctx, cur)
		if err != nil {
			return err
		}
		if amount == nil || amount.Sign() <= 0 {
			return errs.New(errs.CategoryConflict, remittance.ReasonPaymentBlocked, "The obligation assesses nothing to pay.")
		}
		ben, err := s.store.Beneficiary(ctx, in.Beneficiary)
		if err != nil {
			return err
		}
		if ben.State == remittance.BeneficiarySuperseded {
			return errs.New(errs.CategoryConflict, remittance.ReasonPaymentBlocked, "That payee version has been replaced; pay the current version.")
		}
		rid, err := idgen.RemittanceID(s.ids)
		if err != nil {
			return internal(err, "The payment could not be created.")
		}
		loc := cur.Period.Due.Location()
		due := cur.Period.Due
		now := s.now()
		r := remittance.Remittance{
			ID: rid, TenantID: sc.Tenant(), Obligation: cur.ID, ObligationKey: cur.BusinessKey, LegalEntity: cur.LegalEntity,
			Authority: cur.Authority, PeriodStart: cur.Period.Start, PeriodEnd: cur.Period.End,
			PaymentDue: time.Date(due.Year(), due.Month(), due.Day(), 23, 59, 59, 999999000, loc), Timezone: loc.String(),
			Amount: *amount, Beneficiary: ben.ID, FundingRef: strings.TrimSpace(in.FundingRef),
			PaymentReference: strings.TrimSpace(in.PaymentReference),
			ExecutionDate:    time.Date(in.ExecutionDate.Year(), in.ExecutionDate.Month(), in.ExecutionDate.Day(), 0, 0, 0, 0, loc),
			Mode:             in.Mode, Supersedes: supersedes, CreatedAt: now, CreatedBy: sc.Subject(),
		}
		if err := r.Validate(); err != nil {
			return errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue, strings.TrimPrefix(err.Error(), "remittance: ")+".")
		}
		if err := r.Seal(); err != nil {
			return internal(err, "The payment instruction could not be hashed.")
		}
		if err := s.store.Create(ctx, r, remittance.Step{Seq: 1, Kind: remittance.StepCreated, Status: remittance.StatusApprovalRequired,
			RecordedAt: now, RecordedBy: sc.Subject()}); err != nil {
			return err
		}
		if cur.Status == obligation.StatusAccepted {
			if err := s.payments.advancePayment(ctx, cur.BusinessKey, obligation.StatusPaymentDue, sc.Subject()); err != nil {
				return err
			}
		}
		created = true
		out, err = s.store.Remittance(ctx, rid)
		return err
	})
	if err != nil {
		return RemittanceView{}, false, err
	}
	v, err := s.view(ctx, out)
	return v, created, err
}

// Remittance reads one payment as of now.
func (s *RemittanceService) Remittance(ctx context.Context, remittanceID id.RemittanceID) (RemittanceView, error) {
	if _, err := requireRoleOrSystem(ctx, remittanceReaders...); err != nil {
		return RemittanceView{}, err
	}
	r, err := s.store.Remittance(ctx, remittanceID)
	if err != nil {
		return RemittanceView{}, err
	}
	return s.view(ctx, r)
}

// Remittances lists the tenant's payments: overdue first, then by due date.
func (s *RemittanceService) Remittances(ctx context.Context, status remittance.Status, limit int) ([]RemittanceView, error) {
	if _, err := requireRoleOrSystem(ctx, remittanceReaders...); err != nil {
		return nil, err
	}
	rows, err := s.store.List(ctx, limit)
	if err != nil {
		return nil, err
	}
	var out []RemittanceView
	for _, r := range rows {
		if status != "" && r.Status != status {
			continue
		}
		v, err := s.view(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	slices.SortStableFunc(out, func(a, b RemittanceView) int {
		switch {
		case a.Overdue && !b.Overdue:
			return -1
		case b.Overdue && !a.Overdue:
			return 1
		}
		return 0
	})
	return out, nil
}

// ApproveRemittance binds the exact instruction hash. The approver did not
// make the instruction, and did not enter the payee version it pays
// (ZTAX-REM-REQ-0024, -0026, -0031).
func (s *RemittanceService) ApproveRemittance(ctx context.Context, remittanceID id.RemittanceID, hash canonical.Digest, note string) (RemittanceView, error) {
	sc, err := requirePerson(ctx, remittanceApprovers...)
	if err != nil {
		return RemittanceView{}, err
	}
	return s.act(ctx, remittanceID, func(ctx context.Context, r remittance.Remittance) (remittance.Step, error) {
		ben, err := s.store.Beneficiary(ctx, r.Beneficiary)
		if err != nil {
			return remittance.Step{}, err
		}
		if ok, why := ben.Payable(r.Amount.Currency()); !ok {
			return remittance.Step{}, errs.New(errs.CategoryPolicy, remittance.ReasonPaymentBlocked, why+" (ZTAX-REM-REQ-0015)")
		}
		if ben.CreatedBy == sc.Subject() {
			return remittance.Step{}, errs.New(errs.CategoryPolicy, remittance.ReasonMakerChecker,
				"The person who entered this payee version cannot approve a payment to it (ZTAX-REM-REQ-0031).")
		}
		return r.Approve(hash, strings.TrimSpace(note), sc.Subject(), s.now())
	})
}

// SubmitRemittance records the approved instruction leaving for the
// customer's provider, after revalidating it: the payee version is still
// payable, and the obligation still owes exactly the instructed amount
// (ZTAX-REM-REQ-0018 to -0022, -0072).
func (s *RemittanceService) SubmitRemittance(ctx context.Context, remittanceID id.RemittanceID, note string) (RemittanceView, error) {
	sc, err := requirePerson(ctx, remittanceOperators...)
	if err != nil {
		return RemittanceView{}, err
	}
	return s.act(ctx, remittanceID, func(ctx context.Context, r remittance.Remittance) (remittance.Step, error) {
		attempt, err := idgen.SubmissionAttemptID(s.ids)
		if err != nil {
			return remittance.Step{}, internal(err, "The submission could not be recorded.")
		}
		step, err := r.Submit(attempt, strings.TrimSpace(note), sc.Subject(), s.now())
		if err != nil {
			return remittance.Step{}, err
		}
		ben, err := s.store.Beneficiary(ctx, r.Beneficiary)
		if err != nil {
			return remittance.Step{}, err
		}
		if ok, why := ben.Payable(r.Amount.Currency()); !ok {
			return remittance.Step{}, errs.New(errs.CategoryPolicy, remittance.ReasonPaymentBlocked, why+" (ZTAX-REM-REQ-0015)")
		}
		cur, err := s.obligations.Current(ctx, r.ObligationKey)
		if err != nil {
			return remittance.Step{}, err
		}
		if cur.Status != obligation.StatusPaymentDue {
			return remittance.Step{}, errs.New(errs.CategoryConflict, remittance.ReasonPaymentBlocked,
				fmt.Sprintf("The obligation is %s, not payment-due.", cur.Status))
		}
		owed, err := s.liability(ctx, cur)
		if err != nil {
			return remittance.Step{}, err
		}
		if owed == nil || !sameMoney(&r.Amount, *owed) {
			return remittance.Step{}, errs.New(errs.CategoryConflict, remittance.ReasonPaymentBlocked,
				"The liability has changed since this instruction was made; cancel it and make a new one (ZTAX-REM-REQ-0008).")
		}
		return step, nil
	})
}

// StatusInput is what a provider reported.
type StatusInput struct {
	Status      remittance.Status
	Evidence    remittance.Evidence
	ProviderRef string
	Raw         string
	Note        string
}

// RecordStatus records a provider status. Settlement pays the obligation.
func (s *RemittanceService) RecordStatus(ctx context.Context, remittanceID id.RemittanceID, in StatusInput) (RemittanceView, error) {
	sc, err := requirePerson(ctx, remittanceOperators...)
	if err != nil {
		return RemittanceView{}, err
	}
	return s.act(ctx, remittanceID, func(ctx context.Context, r remittance.Remittance) (remittance.Step, error) {
		step, err := r.RecordStatus(in.Status, in.Evidence, in.ProviderRef, in.Raw, strings.TrimSpace(in.Note), sc.Subject(), s.now())
		if err != nil {
			return remittance.Step{}, err
		}
		return step, s.settle(ctx, r, step, sc.Subject())
	})
}

// ResolveRemittance settles an uncertain payment with what was established.
func (s *RemittanceService) ResolveRemittance(ctx context.Context, remittanceID id.RemittanceID, res remittance.Resolution,
	evidence remittance.Evidence, providerRef, raw string) (RemittanceView, error) {
	sc, err := requirePerson(ctx, remittanceOperators...)
	if err != nil {
		return RemittanceView{}, err
	}
	return s.act(ctx, remittanceID, func(ctx context.Context, r remittance.Remittance) (remittance.Step, error) {
		step, err := r.Resolve(res, evidence, providerRef, raw, sc.Subject(), s.now())
		if err != nil {
			return remittance.Step{}, err
		}
		return step, s.settle(ctx, r, step, sc.Subject())
	})
}

// ReturnRemittance records value returned after settlement. The obligation
// is not un-paid by it: its state machine has no edge back from PAID, and a
// return is reconciled and repaid through a new instruction, which is
// recorded against it.
func (s *RemittanceService) ReturnRemittance(ctx context.Context, remittanceID id.RemittanceID, providerRef, raw string) (RemittanceView, error) {
	sc, err := requirePerson(ctx, remittanceOperators...)
	if err != nil {
		return RemittanceView{}, err
	}
	return s.act(ctx, remittanceID, func(_ context.Context, r remittance.Remittance) (remittance.Step, error) {
		return r.Return(providerRef, raw, sc.Subject(), s.now())
	})
}

// CancelRemittance withdraws an instruction that has not been sent.
func (s *RemittanceService) CancelRemittance(ctx context.Context, remittanceID id.RemittanceID, reason string) (RemittanceView, error) {
	sc, err := requirePerson(ctx, remittanceOperators...)
	if err != nil {
		return RemittanceView{}, err
	}
	return s.act(ctx, remittanceID, func(_ context.Context, r remittance.Remittance) (remittance.Step, error) {
		return r.Cancel(strings.TrimSpace(reason), sc.Subject(), s.now())
	})
}

// settle pays the obligation when a step reaches SETTLED.
func (s *RemittanceService) settle(ctx context.Context, r remittance.Remittance, step remittance.Step, by id.UserID) error {
	if step.Status != remittance.StatusSettled {
		return nil
	}
	if s.payments == nil {
		return errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable, "Obligations are not wired in this cell.")
	}
	return s.payments.advancePayment(ctx, r.ObligationKey, obligation.StatusPaid, by)
}

// liability is what an obligation owes now: the sum of its contributions.
func (s *RemittanceService) liability(ctx context.Context, o obligation.Obligation) (*fiscal.Money, error) {
	sums, err := s.obligations.Assessed(ctx, []string{o.BusinessKey})
	if err != nil {
		return nil, err
	}
	if t, ok := sums[o.BusinessKey]; ok {
		return &t, nil
	}
	return o.Assessed, nil
}

func (s *RemittanceService) act(ctx context.Context, remittanceID id.RemittanceID,
	fn func(context.Context, remittance.Remittance) (remittance.Step, error)) (RemittanceView, error) {
	var out remittance.Remittance
	err := s.inTx(ctx, func(ctx context.Context) error {
		r, err := s.store.Remittance(ctx, remittanceID)
		if err != nil {
			return err
		}
		if err := s.store.Lock(ctx, r.ObligationKey); err != nil {
			return err
		}
		if r, err = s.store.Remittance(ctx, remittanceID); err != nil {
			return err
		}
		step, err := fn(ctx, r)
		if err != nil {
			return err
		}
		if err := s.store.AppendStep(ctx, r.ID, step); err != nil {
			return err
		}
		out, err = s.store.Remittance(ctx, remittanceID)
		return err
	})
	if err != nil {
		return RemittanceView{}, err
	}
	return s.view(ctx, out)
}

// view attaches what a payment's state means now.
func (s *RemittanceService) view(ctx context.Context, r remittance.Remittance) (RemittanceView, error) {
	now := s.now()
	v := RemittanceView{Remittance: r, Overdue: r.Overdue(now)}
	cur, err := s.obligations.Current(ctx, r.ObligationKey)
	if err != nil {
		return RemittanceView{}, err
	}
	v.ObligationStatus = cur.Status
	if v.Beneficiary, err = s.store.Beneficiary(ctx, r.Beneficiary); err != nil {
		return RemittanceView{}, err
	}
	check := func(dim string, ok bool, detail string) {
		v.Readiness = append(v.Readiness, PackageReadiness{Dimension: dim, Ready: ok, Detail: detail})
	}
	sent := r.Status.Sent()

	switch owed, err := s.liability(ctx, cur); {
	case err != nil:
		return RemittanceView{}, err
	case sent:
		check("LIABILITY_READY", true, "Sent against the liability as it stood.")
	case cur.Status != obligation.StatusPaymentDue && cur.Status != obligation.StatusAccepted:
		check("LIABILITY_READY", false, fmt.Sprintf("The obligation is %s, not payment-due.", cur.Status))
	case owed == nil || !sameMoney(&r.Amount, *owed):
		check("LIABILITY_READY", false, "The liability has changed since this instruction was made.")
	default:
		check("LIABILITY_READY", true, fmt.Sprintf("The obligation owes %s %s.", r.Amount.CanonicalString(), r.Amount.Currency()))
	}
	payable, why := v.Beneficiary.Payable(r.Amount.Currency())
	check("BENEFICIARY_READY", payable || sent, why)
	check("FUNDING_READY", remittance.IsSecureRef(r.FundingRef),
		"The customer's own account at its provider, by reference. ZoikoTax holds no funds.")
	check("REFERENCE_READY", remittance.ReferenceValid(v.Beneficiary.ReferenceRule, r.PaymentReference),
		"The payment reference against the authority's format.")
	switch {
	case r.Approval() != nil || sent:
		check("AUTHORIZATION_READY", true, "The exact instruction is approved.")
	default:
		check("AUTHORIZATION_READY", false, "Awaiting approval by someone other than its maker.")
	}
	dueDay := time.Date(r.PaymentDue.Year(), r.PaymentDue.Month(), r.PaymentDue.Day(), 0, 0, 0, 0, r.PaymentDue.Location())
	check("DEADLINE_READY", !r.ExecutionDate.After(dueDay),
		fmt.Sprintf("Executes %s; due %s.", r.ExecutionDate.Format(time.DateOnly), dueDay.Format(time.DateOnly)))

	v.Ready = true
	for _, c := range v.Readiness {
		v.Ready = v.Ready && c.Ready
	}
	return v, nil
}

func (s *RemittanceService) inTx(ctx context.Context, fn func(context.Context) error) error {
	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(txCtx) }()
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(txCtx)
}
