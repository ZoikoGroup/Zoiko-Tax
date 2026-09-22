// Package subledger is the fiscal subledger: double-entry, append-only.
//
// Nothing here is ever corrected in place. A correction is a reversing entry
// plus a new one, which is both what an accountant expects and what ADR-0003
// §2.1 requires — the application's database role holds INSERT and SELECT on
// this table and nothing else, so an UPDATE would fail at the database even if
// somebody wrote one.
//
// The invariant the package exists to hold is that every transaction balances.
// It is checked here, in the domain, before anything is written, because a
// database constraint cannot express "these rows, taken together, sum to zero"
// across an insert of several rows.
package subledger

import (
	"fmt"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Account is a subledger account code.
//
// The chart of accounts is reference data with an owner, so these are the small
// fixed set the fiscal core itself posts to. A country pack that needs more
// supplies them as content rather than as new constants here.
type Account string

// The core accounts.
const (
	// AccountTaxPayable is the liability to an authority.
	AccountTaxPayable Account = "TAX_PAYABLE"
	// AccountTaxReceivable is tax recoverable from an authority.
	AccountTaxReceivable Account = "TAX_RECEIVABLE"
	// AccountRevenue is the net amount of a supply.
	AccountRevenue Account = "REVENUE"
	// AccountCustomerControl is the total owed by a customer.
	AccountCustomerControl Account = "CUSTOMER_CONTROL"
	// AccountSuspense holds an amount whose account is not yet determined. An
	// entry here is a finding, not a resting place.
	AccountSuspense Account = "SUSPENSE"
)

// Entry is one posting.
//
// Amount is signed: positive is a debit, negative a credit. One signed column
// rather than two unsigned ones, so that "does this transaction balance" is a
// sum rather than a difference of two sums — and so that a reversal is a
// negation rather than a swap of two fields, which is the operation people get
// wrong.
type Entry struct {
	ID       id.LedgerEntryID
	TenantID id.TenantID

	// TransactionKey groups the entries that must balance together.
	TransactionKey string
	// SourceDecisionID binds the posting to the determination that caused it,
	// so "why is this here" is answerable.
	SourceDecisionID *id.DecisionID

	Account        Account
	JurisdictionID string
	Amount         fiscal.Money

	EventTime  time.Time
	RecordedAt time.Time

	// Reverses names the entry this one cancels, for a correction.
	Reverses *id.LedgerEntryID
}

// Transaction is a balanced set of entries.
type Transaction struct {
	Key     string
	Entries []Entry
}

// Validate refuses a transaction that does not balance.
//
// Balance is asserted per currency, not across the set. A transaction holding
// EUR and USD entries that happen to sum to zero in some notional total is not
// balanced; it is two transactions that have been confused for one, and summing
// across currencies would require a rate this package has no business holding.
func (t Transaction) Validate() error {
	if t.Key == "" {
		return fmt.Errorf("subledger: transaction has no key")
	}
	if len(t.Entries) < 2 {
		// A single entry cannot balance. Refusing it here catches the common
		// mistake of posting the debit and forgetting the credit, which would
		// otherwise be found by a reconciliation weeks later.
		return fmt.Errorf("subledger: transaction %s has %d entries; a balanced transaction needs at least 2", t.Key, len(t.Entries))
	}

	totals := map[fiscal.Currency]fiscal.Money{}
	for i, e := range t.Entries {
		if e.TransactionKey != t.Key {
			return fmt.Errorf("subledger: entry %d belongs to transaction %q, not %q", i, e.TransactionKey, t.Key)
		}
		if e.Account == "" {
			return fmt.Errorf("subledger: entry %d names no account", i)
		}
		currency := e.Amount.Currency()
		if currency == "" {
			return fmt.Errorf("subledger: entry %d has no currency", i)
		}

		running, seen := totals[currency]
		if !seen {
			totals[currency] = e.Amount
			continue
		}
		sum, err := running.Add(e.Amount)
		if err != nil {
			return fmt.Errorf("subledger: entry %d: %w", i, err)
		}
		totals[currency] = sum
	}

	// Sorted so the error names the same currency every time for one input.
	currencies := make([]fiscal.Currency, 0, len(totals))
	for c := range totals {
		currencies = append(currencies, c)
	}
	sort.Slice(currencies, func(i, j int) bool { return currencies[i] < currencies[j] })

	for _, c := range currencies {
		if !totals[c].IsZero() {
			return fmt.Errorf("subledger: transaction %s does not balance in %s: residual %s",
				t.Key, c, totals[c].CanonicalString())
		}
	}
	return nil
}

// Currencies returns the currencies this transaction touches, sorted.
func (t Transaction) Currencies() []fiscal.Currency {
	seen := map[fiscal.Currency]struct{}{}
	for _, e := range t.Entries {
		seen[e.Amount.Currency()] = struct{}{}
	}
	out := make([]fiscal.Currency, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Reverse builds the entries that cancel a transaction.
//
// Every amount is negated and every entry names what it reverses. The result is
// a new transaction that balances by construction — if the original balanced,
// the negation of each entry balances too — and the original rows are untouched,
// which is what append-only means in practice.
func (t Transaction) Reverse(key string, at time.Time, newIDs []id.LedgerEntryID) (Transaction, error) {
	if len(newIDs) != len(t.Entries) {
		return Transaction{}, fmt.Errorf("subledger: reversing %d entries needs %d identifiers, got %d",
			len(t.Entries), len(t.Entries), len(newIDs))
	}
	out := Transaction{Key: key, Entries: make([]Entry, 0, len(t.Entries))}
	for i, e := range t.Entries {
		zero, err := fiscal.ParseMoney("0", e.Amount.Currency())
		if err != nil {
			return Transaction{}, err
		}
		negated, err := zero.Sub(e.Amount)
		if err != nil {
			return Transaction{}, err
		}
		prior := e.ID
		out.Entries = append(out.Entries, Entry{
			ID:               newIDs[i],
			TenantID:         e.TenantID,
			TransactionKey:   key,
			SourceDecisionID: e.SourceDecisionID,
			Account:          e.Account,
			JurisdictionID:   e.JurisdictionID,
			Amount:           negated,
			EventTime:        e.EventTime,
			RecordedAt:       at.UTC(),
			Reverses:         &prior,
		})
	}
	return out, nil
}

// Canonical renders a transaction for the evidence record.
func (t Transaction) Canonical() canonical.Value {
	items := make([]canonical.Value, 0, len(t.Entries))
	for _, e := range t.Entries {
		reverses := canonical.Absent()
		if e.Reverses != nil {
			reverses = canonical.String(e.Reverses.String())
		}
		items = append(items, canonical.Object(
			canonical.F("account", canonical.String(string(e.Account))),
			canonical.F("jurisdiction", canonical.OptString(e.JurisdictionID)),
			canonical.F("amount", canonical.Money(e.Amount)),
			canonical.F("currency", canonical.String(string(e.Amount.Currency()))),
			canonical.F("reverses", reverses),
		))
	}
	return canonical.Object(
		canonical.F("transactionKey", canonical.String(t.Key)),
		// Entry order is the order they were posted and is part of the record.
		canonical.F("entries", canonical.Array(items...)),
	)
}
