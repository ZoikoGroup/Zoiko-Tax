package subledger

import (
	"fmt"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// The Tax Control Subledger of ZTAX-FIN-001 §9–§11. Transaction and Entry
// above are the W1 balanced-set primitive; Journal is the TCSL's authoritative
// record built on the same invariant, with the scope, source and sign
// semantics FIN-001 requires of every posted journal.
//
// The TCSL is a control ledger. It proves tax liability, recovery and control
// movements; it is not the customer's general ledger and holds none of its
// accounts (ZTAX-FIN-REQ-0033, -0034).

// The canonical TCSL control accounts (FIN-001 §9). A customer's chart of
// accounts maps onto these through a GLMapping; it never replaces them
// (ZTAX-FIN-REQ-0051).
const (
	AccountTaxCollectedLiability Account = "TAX_COLLECTED_LIABILITY"
	AccountTaxAccruedLiability   Account = "TAX_ACCRUED_LIABILITY"
	AccountTaxRecoverable        Account = "TAX_RECOVERABLE"
	AccountTaxReceivableControl  Account = "TAX_RECEIVABLE_CONTROL"
	AccountTaxCashClearing       Account = "TAX_CASH_CLEARING"
	AccountTaxReturnClearing     Account = "TAX_RETURN_CLEARING"
	AccountTaxRemittanceClearing Account = "TAX_REMITTANCE_CLEARING"
	AccountTaxAdjustmentControl  Account = "TAX_ADJUSTMENT_CONTROL"
	AccountFXControl             Account = "FX_CONTROL"
	AccountRoundingControl       Account = "ROUNDING_CONTROL"
	AccountSuspenseException     Account = "SUSPENSE_EXCEPTION"
	AccountCustomerGLBridge      Account = "CUSTOMER_GL_BRIDGE"
)

var tcslAccounts = map[Account]bool{
	AccountTaxCollectedLiability: true, AccountTaxAccruedLiability: true, AccountTaxRecoverable: true,
	AccountTaxReceivableControl: true, AccountTaxCashClearing: true, AccountTaxReturnClearing: true,
	AccountTaxRemittanceClearing: true, AccountTaxAdjustmentControl: true, AccountFXControl: true,
	AccountRoundingControl: true, AccountSuspenseException: true, AccountCustomerGLBridge: true,
}

// Canonical reports whether a is a TCSL control account.
func (a Account) Canonical() bool { return tcslAccounts[a] }

// Side is a journal line's side. A journal line carries a positive amount and
// a side, never a signed amount whose sign is a convention somebody has to
// know (ZTAX-FIN-REQ-0044, -0108).
type Side string

// The sides.
const (
	Debit  Side = "DEBIT"
	Credit Side = "CREDIT"
)

// JournalType is FIN-001 §10's journal type.
type JournalType string

// The journal types.
const (
	JournalInvoice          JournalType = "INVOICE"
	JournalCredit           JournalType = "CREDIT"
	JournalRefund           JournalType = "REFUND"
	JournalLiabilityAccrual JournalType = "LIABILITY_ACCRUAL"
	JournalReturn           JournalType = "RETURN"
	JournalRemittance       JournalType = "REMITTANCE"
	JournalFX               JournalType = "FX"
	JournalRounding         JournalType = "ROUNDING"
	JournalMigration        JournalType = "MIGRATION"
	JournalAdjustment       JournalType = "ADJUSTMENT"
)

func (t JournalType) valid() bool {
	switch t {
	case JournalInvoice, JournalCredit, JournalRefund, JournalLiabilityAccrual, JournalReturn,
		JournalRemittance, JournalFX, JournalRounding, JournalMigration, JournalAdjustment:
		return true
	}
	return false
}

// SourceEvent is the authoritative event a journal posts (ZTAX-FIN-REQ-0041).
type SourceEvent struct {
	Kind string
	ID   string
}

// JournalLine is one debit or credit.
type JournalLine struct {
	Account Account
	Side    Side
	// Amount is positive; the side says which way it moves.
	Amount       fiscal.Money
	Authority    string
	Jurisdiction string
	Decision     *id.DecisionID
	Document     *id.FiscalDocumentID
	Obligation   *id.ObligationID
}

// Journal is one balanced, posted TCSL journal.
type Journal struct {
	ID          id.JournalID
	TenantID    id.TenantID
	LegalEntity id.LegalEntityID
	Type        JournalType
	Source      SourceEvent
	PostingDate time.Time
	// LegalPeriod is the reporting or tax period affected, which a true-up
	// keeps pointing at the original period (ZTAX-FIN-REQ-0095).
	LegalPeriod string
	Currency    fiscal.Currency
	Lines       []JournalLine
	// ReversalOf names the journal this one reverses (ZTAX-FIN-REQ-0037).
	ReversalOf *id.JournalID
	// Profile is the posting-rule version that produced the journal.
	Profile ProfileRef
	// Amendment marks a journal posted through the amendment workflow, the
	// only route into a hard-closed period (ZTAX-FIN-REQ-0090).
	Amendment bool
	// MigrationSource and Cutover are required on a MIGRATION journal
	// (ZTAX-FIN-REQ-0114).
	MigrationSource string
	Cutover         *time.Time
}

// Validate applies the posting invariants of FIN-001 §11. A journal that fails
// it is blocked before posting (ZTAX-FIN-REQ-0096).
func (j Journal) Validate() error {
	switch {
	case j.ID.IsZero():
		return fmt.Errorf("subledger: journal has no id")
	case j.TenantID.IsZero() || j.LegalEntity.IsZero():
		return fmt.Errorf("subledger: journal %s carries no tenant or no legal entity", j.ID)
	case !j.Type.valid():
		return fmt.Errorf("subledger: journal %s has type %q", j.ID, j.Type)
	case j.Source.Kind == "" || j.Source.ID == "":
		return fmt.Errorf("subledger: journal %s identifies no source event", j.ID)
	case j.PostingDate.IsZero() || j.LegalPeriod == "":
		return fmt.Errorf("subledger: journal %s has no posting date or no legal period", j.ID)
	case j.Currency == "":
		return fmt.Errorf("subledger: journal %s has no currency", j.ID)
	case len(j.Lines) < 2:
		return fmt.Errorf("subledger: journal %s has %d lines; a balanced journal needs at least 2", j.ID, len(j.Lines))
	}
	if j.Type == JournalMigration && (j.MigrationSource == "" || j.Cutover == nil) {
		return fmt.Errorf("subledger: migration journal %s carries no source provenance or no cutover date", j.ID)
	}
	if j.Type != JournalMigration && (j.MigrationSource != "" || j.Cutover != nil) {
		return fmt.Errorf("subledger: journal %s carries migration provenance and is a %s", j.ID, j.Type)
	}
	debits, err := fiscal.ParseMoney("0", j.Currency)
	if err != nil {
		return err
	}
	credits := debits
	for i, l := range j.Lines {
		if !l.Account.Canonical() {
			return fmt.Errorf("subledger: journal %s line %d posts to %q, which is not a TCSL account", j.ID, i, l.Account)
		}
		if l.Amount.Currency() != j.Currency {
			// ZTAX-FIN-REQ-0040: one currency per journal. A document in two
			// currencies posts two journals.
			return fmt.Errorf("subledger: journal %s is in %s and line %d is in %s", j.ID, j.Currency, i, l.Amount.Currency())
		}
		if l.Amount.Sign() <= 0 {
			return fmt.Errorf("subledger: journal %s line %d has amount %s; amounts are positive and the side carries direction", j.ID, i, l.Amount.CanonicalString())
		}
		switch l.Side {
		case Debit:
			debits, err = debits.Add(l.Amount)
		case Credit:
			credits, err = credits.Add(l.Amount)
		default:
			return fmt.Errorf("subledger: journal %s line %d has side %q", j.ID, i, l.Side)
		}
		if err != nil {
			return err
		}
	}
	if c, err := debits.Cmp(credits); err != nil || c != 0 {
		return errs.New(errs.CategoryValidation, errs.ReasonInvalidValue,
			fmt.Sprintf("Journal %s does not balance: debits %s, credits %s in %s.", j.ID, debits.CanonicalString(), credits.CanonicalString(), j.Currency))
	}
	return nil
}

// Reverse builds the journal that neutralises j: every line on the other side,
// linked by ReversalOf. j itself is not touched — posted journals are
// append-only, and a correction is a reversal plus a replacement
// (ZTAX-FIN-REQ-0036 to -0038).
func (j Journal) Reverse(newID id.JournalID, postingDate time.Time, source SourceEvent) (Journal, error) {
	if newID == j.ID {
		return Journal{}, fmt.Errorf("subledger: a reversal reuses journal id %s", j.ID)
	}
	out := j
	out.ID = newID
	out.Source = source
	out.PostingDate = postingDate.UTC()
	prior := j.ID
	out.ReversalOf = &prior
	out.Lines = make([]JournalLine, len(j.Lines))
	for i, l := range j.Lines {
		r := l
		if l.Side == Debit {
			r.Side = Credit
		} else {
			r.Side = Debit
		}
		out.Lines[i] = r
	}
	if err := out.Validate(); err != nil {
		return Journal{}, err
	}
	return out, nil
}

// PeriodState is FIN-001 §21's fiscal period state.
type PeriodState string

// The period states.
const (
	PeriodOpen            PeriodState = "OPEN"
	PeriodSoftClose       PeriodState = "SOFT_CLOSE"
	PeriodHardClose       PeriodState = "HARD_CLOSE"
	PeriodReopened        PeriodState = "REOPENED"
	PeriodAmendmentActive PeriodState = "AMENDMENT_ACTIVE"
	PeriodSealed          PeriodState = "SEALED"
)

// CheckPostable says whether a journal may post into a period in a state.
//
// OPEN and REOPENED take anything. SOFT_CLOSE takes only adjustment and
// rounding journals — the approved late adjustments. HARD_CLOSE and SEALED
// take nothing by the normal path (ZTAX-FIN-REQ-0104); AMENDMENT_ACTIVE takes
// only amendment journals (ZTAX-FIN-REQ-0090).
func CheckPostable(j Journal, state PeriodState) error {
	switch state {
	case PeriodOpen, PeriodReopened:
		return nil
	case PeriodSoftClose:
		if j.Type == JournalAdjustment || j.Type == JournalRounding {
			return nil
		}
	case PeriodAmendmentActive:
		if j.Amendment {
			return nil
		}
	case PeriodHardClose, PeriodSealed:
	default:
		return fmt.Errorf("subledger: period state %q", state)
	}
	return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
		fmt.Sprintf("Period %s is %s; a %s journal cannot post into it by the normal path.", j.LegalPeriod, state, j.Type))
}

// BalanceKey is one control balance's dimensions.
type BalanceKey struct {
	Account  Account
	Currency fiscal.Currency
}

// Balance is one control balance: debits and credits kept apart, never netted
// into a sign.
type Balance struct {
	Debits  fiscal.Money
	Credits fiscal.Money
}

// Balances summarises journals into control balances computed from their
// lines, so a summary always reconciles to the immutable lines it came from
// (ZTAX-FIN-REQ-0121).
func Balances(journals []Journal) (map[BalanceKey]Balance, error) {
	out := map[BalanceKey]Balance{}
	for _, j := range journals {
		for _, l := range j.Lines {
			k := BalanceKey{Account: l.Account, Currency: l.Amount.Currency()}
			b, ok := out[k]
			if !ok {
				zero := l.Amount.Zero()
				b = Balance{Debits: zero, Credits: zero}
			}
			var err error
			if l.Side == Debit {
				b.Debits, err = b.Debits.Add(l.Amount)
			} else {
				b.Credits, err = b.Credits.Add(l.Amount)
			}
			if err != nil {
				return nil, err
			}
			out[k] = b
		}
	}
	return out, nil
}

// Orphans returns the journals whose source event is not among known, sorted
// by id (ZTAX-FIN-REQ-0099).
func Orphans(journals []Journal, known map[SourceEvent]bool) []id.JournalID {
	var out []id.JournalID
	for _, j := range journals {
		if !known[j.Source] {
			out = append(out, j.ID)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].String() < out[k].String() })
	return out
}

// SuspenseItem is one unresolved suspense line, with the owner and age FIN-001
// §23 requires of it (ZTAX-FIN-REQ-0046).
type SuspenseItem struct {
	Journal  id.JournalID
	Line     int
	Owner    id.UserID
	OpenedAt time.Time
	// SLA is how long it may stay open before it is SUSPENSE_AGED.
	SLA time.Duration
}

// Validate refuses a suspense item nobody owns.
func (s SuspenseItem) Validate() error {
	if s.Owner.IsZero() {
		return fmt.Errorf("subledger: suspense on journal %s line %d has no owner", s.Journal, s.Line)
	}
	if s.OpenedAt.IsZero() || s.SLA <= 0 {
		return fmt.Errorf("subledger: suspense on journal %s line %d has no age policy", s.Journal, s.Line)
	}
	return nil
}

// Aged reports whether the item has outlived its SLA as of an instant.
func (s SuspenseItem) Aged(asOf time.Time) bool { return asOf.Sub(s.OpenedAt) > s.SLA }
