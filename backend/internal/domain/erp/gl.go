package erp

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/reconciliation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// The subledger-to-GL reconciliation: R6 of ZTAX-FIN-001 §17, and LED-18's
// "compare GL balance".
//
// What the ERP holds is reported to ZoikoTax as a snapshot of GL balances —
// the period's activity on each mapped GL account, debits and credits apart,
// with the ERP report it came from. Nothing here reads an ERP. A
// reconciliation compares the snapshot, account by account, with what the
// Tax Control Subledger's journals of the same legal entity, period and
// currency put on the TCSL accounts mapped to each GL account. The variance
// is exact and kept whatever it is; there is no tolerance (ZTAX-FIN-REQ-0080,
// -0081).
//
// A reconciliation is HELD while any of the period's journals is not in a
// batch the ERP has posted: unexported, awaiting dispatch or posting, of
// unknown outcome, or rejected with its correction open (LED-23 "ERP posting
// failure"). A held reconciliation is never reported clean, whatever its
// items say. A variance is not cleared by a note; it is cleared by fixing the
// source and running a new reconciliation over a new snapshot (LED-21 "Attach
// remediation proof").

// GLBalance is one GL account's period activity, as the ERP reported it.
type GLBalance struct {
	GLAccount string
	Debits    fiscal.Money
	Credits   fiscal.Money
}

// Net is debits less credits.
func (b GLBalance) Net() (fiscal.Money, error) { return b.Debits.Sub(b.Credits) }

// GLSnapshot is one set of GL balances an ERP reported for a period and
// currency, recorded with the report it came from.
type GLSnapshot struct {
	ID       id.GLSnapshotID
	TenantID id.TenantID
	Bridge   id.ERPBridgeID
	Period   string
	Currency fiscal.Currency
	// AsOf is when the ERP's figures were taken.
	AsOf time.Time
	// SourceRef names the ERP report or extract; SourceDigest is its
	// checksum, when the report is kept as a file.
	SourceRef    string
	SourceDigest string
	// Lines are sorted by GL account.
	Lines      []GLBalance
	Digest     canonical.Digest
	RecordedAt time.Time
	RecordedBy id.UserID
}

// MaxSnapshotLines bounds one snapshot.
const MaxSnapshotLines = 500

var decimalShape = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)

// ParseAmount reads a reported debit or credit total: a canonical,
// non-negative decimal string, never a number.
func ParseAmount(field, amount string, currency fiscal.Currency) (fiscal.Money, error) {
	if !decimalShape.MatchString(amount) {
		return fiscal.Money{}, errs.Invalid(field, errs.ReasonInvalidValue,
			"An amount is a canonical, non-negative decimal string, such as \"1250.00\"; the side carries direction.")
	}
	m, err := fiscal.ParseMoney(amount, currency)
	if err != nil {
		return fiscal.Money{}, errs.Invalid(field, errs.ReasonInvalidValue, "The amount is not representable exactly.")
	}
	return m, nil
}

// Validate refuses a snapshot that cannot be recorded.
func (s GLSnapshot) Validate() error {
	if !ValidPeriod(s.Period) {
		return errs.Invalid("period", errs.ReasonInvalidValue, "The period is a month, YYYY-MM.")
	}
	if !ValidCurrency(string(s.Currency)) {
		return errs.Invalid("currency", errs.ReasonInvalidValue, "The currency is an ISO 4217 code, such as USD.")
	}
	if s.AsOf.IsZero() {
		return errs.Invalid("asOf", errs.ReasonMissingField, "A snapshot says when the ERP's figures were taken.")
	}
	if err := text("sourceRef", s.SourceRef, true, MaxRef); err != nil {
		return err
	}
	if s.SourceDigest != "" && !ValidDigestRef(s.SourceDigest) {
		return errs.Invalid("sourceDigest", errs.ReasonInvalidValue, "A source digest is up to 200 characters of a digest's alphabet.")
	}
	if len(s.Lines) == 0 || len(s.Lines) > MaxSnapshotLines {
		return errs.Invalid("balances", errs.ReasonInvalidValue, fmt.Sprintf("A snapshot reports between 1 and %d GL accounts.", MaxSnapshotLines))
	}
	seen := map[string]bool{}
	for _, l := range s.Lines {
		if !ValidGLAccount(l.GLAccount) {
			return errs.Invalid("balances.glAccount", errs.ReasonInvalidValue,
				"A GL account is 1 to 60 letters, digits, dots, underscores, colons, slashes or hyphens.")
		}
		if seen[l.GLAccount] {
			return errs.Invalid("balances.glAccount", errs.ReasonInvalidValue, fmt.Sprintf("%s is reported twice.", l.GLAccount))
		}
		seen[l.GLAccount] = true
		if l.Debits.Currency() != s.Currency || l.Credits.Currency() != s.Currency {
			return errs.Invalid("balances", errs.ReasonInvalidValue, "Every balance is in the snapshot's one currency.")
		}
		if l.Debits.Sign() < 0 || l.Credits.Sign() < 0 {
			return errs.Invalid("balances", errs.ReasonInvalidValue, "Debits and credits are each zero or more; the side carries direction.")
		}
	}
	return nil
}

// Seal sorts the lines and computes the snapshot's digest.
func (s GLSnapshot) Seal() (GLSnapshot, error) {
	s.Lines = slices.Clone(s.Lines)
	slices.SortFunc(s.Lines, func(a, b GLBalance) int { return strings.Compare(a.GLAccount, b.GLAccount) })
	lines := make([]canonical.Value, len(s.Lines))
	for i, l := range s.Lines {
		lines[i] = canonical.Object(
			canonical.F("glAccount", canonical.String(l.GLAccount)),
			canonical.F("debits", canonical.Money(l.Debits)),
			canonical.F("credits", canonical.Money(l.Credits)),
		)
	}
	d, err := canonical.Sum(canonical.Object(
		canonical.F("artifact", canonical.String("erp-gl-snapshot")),
		canonical.F("bridgeId", canonical.String(s.Bridge.String())),
		canonical.F("period", canonical.String(s.Period)),
		canonical.F("currency", canonical.String(string(s.Currency))),
		canonical.F("asOf", canonical.Time(s.AsOf)),
		canonical.F("sourceRef", canonical.String(s.SourceRef)),
		canonical.F("sourceDigest", canonical.OptString(s.SourceDigest)),
		canonical.F("balances", canonical.Array(lines...)),
	))
	if err != nil {
		return GLSnapshot{}, err
	}
	s.Digest = d
	return s, nil
}

// Outcome is a GL reconciliation's verdict.
type Outcome string

// The outcomes. HELD outranks VARIANCES: a held reconciliation is never
// reported as reconciled, and its variances are expected until it is not.
const (
	OutcomeReconciled Outcome = "RECONCILED"
	OutcomeVariances  Outcome = "VARIANCES"
	OutcomeHeld       Outcome = "HELD"
)

// HoldReason is why a period's journals are not all in the GL yet.
type HoldReason string

// The hold reasons, one per journal.
const (
	HoldUnexported       HoldReason = "UNEXPORTED_JOURNALS"
	HoldAwaitingDispatch HoldReason = "AWAITING_DISPATCH"
	HoldAwaitingPosting  HoldReason = "AWAITING_ERP_POSTING"
	HoldOutcomeUnknown   HoldReason = "OUTCOME_UNKNOWN"
	HoldRejected         HoldReason = "REJECTED_CORRECTION_OPEN"
)

// Hold is how many of the period's journals are held for one reason.
type Hold struct {
	Reason   HoldReason
	Journals int
}

// GLItem is one compared GL account: an R6 reconciliation item with the GL
// account and the TCSL accounts mapped to it.
type GLItem struct {
	reconciliation.Item
	GLAccount    string
	TCSLAccounts []subledger.Account
}

// GLReconciliation is one subledger-to-GL reconciliation of a legal period
// and currency, against one snapshot, under one mapping version. It is a
// record: a later reconciliation is a new one.
type GLReconciliation struct {
	ID             id.GLReconciliationID
	TenantID       id.TenantID
	Bridge         id.ERPBridgeID
	Snapshot       id.GLSnapshotID
	SnapshotDigest canonical.Digest
	LegalEntity    id.LegalEntityID
	Period         string
	Currency       fiscal.Currency
	MappingVersion int
	Outcome        Outcome
	Holds          []Hold
	JournalCount   int
	PostedJournals int
	// PopulationDigest is the digest of the period's sorted journal IDs: the
	// subledger population this reconciliation compared.
	PopulationDigest canonical.Digest
	Items            []GLItem
	RanAt            time.Time
	RanBy            id.UserID
}

// ReconcileInput is what a GL reconciliation compares.
type ReconcileInput struct {
	Mapping  Mapping
	Snapshot GLSnapshot
	// Journals are the period's posted journals of the bridge's legal
	// entity in the snapshot's currency.
	Journals []subledger.Journal
	// Batches are the bridge's export batches of the period and currency,
	// in any status.
	Batches []Batch
}

// Reconcile compares the subledger with the snapshot, account by account,
// and says what holds the period. It fills the outcome, holds, counts,
// population digest and items of a GLReconciliation; the caller names it.
func Reconcile(in ReconcileInput) (GLReconciliation, error) {
	cur := in.Snapshot.Currency
	out := GLReconciliation{
		Snapshot: in.Snapshot.ID, SnapshotDigest: in.Snapshot.Digest, Bridge: in.Snapshot.Bridge, Period: in.Snapshot.Period,
		Currency: cur, MappingVersion: in.Mapping.Version, JournalCount: len(in.Journals),
	}
	for _, j := range in.Journals {
		if j.Currency != cur || j.LegalPeriod != in.Snapshot.Period {
			return GLReconciliation{}, fmt.Errorf("erp: journal %s is not a %s journal of %s", j.ID, cur, in.Snapshot.Period)
		}
	}

	// The population.
	ids := make([]string, len(in.Journals))
	for i, j := range in.Journals {
		ids[i] = j.ID.String()
	}
	slices.Sort(ids)
	items := make([]canonical.Value, len(ids))
	for i, s := range ids {
		items[i] = canonical.String(s)
	}
	var err error
	if out.PopulationDigest, err = canonical.Sum(canonical.Object(
		canonical.F("artifact", canonical.String("tcsl-period-population")),
		canonical.F("period", canonical.String(in.Snapshot.Period)),
		canonical.F("currency", canonical.String(string(cur))),
		canonical.F("journals", canonical.Array(items...)),
	)); err != nil {
		return GLReconciliation{}, err
	}

	// What holds it: each journal once, by the batch that carries it.
	carriedBy := map[id.JournalID]Status{}
	rejected := map[id.JournalID]bool{}
	for _, b := range in.Batches {
		for _, j := range b.Journals {
			if b.Status.Live() {
				carriedBy[j] = b.Status
			} else if b.Status == StatusRejected {
				rejected[j] = true
			}
		}
	}
	counts := map[HoldReason]int{}
	for _, j := range in.Journals {
		switch s, ok := carriedBy[j.ID]; {
		case ok && s == StatusERPPosted:
			out.PostedJournals++
		case ok && s == StatusPrepared:
			counts[HoldAwaitingDispatch]++
		case ok && s == StatusOutcomeUnknown:
			counts[HoldOutcomeUnknown]++
		case ok:
			counts[HoldAwaitingPosting]++
		case rejected[j.ID]:
			counts[HoldRejected]++
		default:
			counts[HoldUnexported]++
		}
	}
	for _, r := range []HoldReason{HoldUnexported, HoldAwaitingDispatch, HoldAwaitingPosting, HoldOutcomeUnknown, HoldRejected} {
		if counts[r] > 0 {
			out.Holds = append(out.Holds, Hold{Reason: r, Journals: counts[r]})
		}
	}

	// What the subledger says, per GL account.
	balances, err := subledger.Balances(in.Journals)
	if err != nil {
		return GLReconciliation{}, err
	}
	type expected struct {
		net      fiscal.Money
		accounts []subledger.Account
	}
	byGL := map[string]*expected{}
	for _, am := range in.Mapping.Accounts {
		e, ok := byGL[am.GLAccount]
		if !ok {
			zero, err := fiscal.ParseMoney("0", cur)
			if err != nil {
				return GLReconciliation{}, err
			}
			e = &expected{net: zero}
			byGL[am.GLAccount] = e
		}
		e.accounts = append(e.accounts, am.Account)
	}
	var unmapped []subledger.Account
	for k, b := range balances {
		gl, ok := in.Mapping.GLAccountFor(k.Account)
		net, err := b.Debits.Sub(b.Credits)
		if err != nil {
			return GLReconciliation{}, err
		}
		if !ok {
			unmapped = append(unmapped, k.Account)
			continue
		}
		e := byGL[gl]
		if e.net, err = e.net.Add(net); err != nil {
			return GLReconciliation{}, err
		}
	}
	reported := map[string]GLBalance{}
	for _, l := range in.Snapshot.Lines {
		reported[l.GLAccount] = l
	}

	none := reconciliation.Policy{}
	glAccounts := make([]string, 0, len(byGL)+len(reported))
	for gl := range byGL {
		glAccounts = append(glAccounts, gl)
	}
	for gl := range reported {
		if _, ok := byGL[gl]; !ok {
			glAccounts = append(glAccounts, gl)
		}
	}
	slices.Sort(glAccounts)
	for _, gl := range glAccounts {
		e, mapped := byGL[gl]
		r, isReported := reported[gl]
		var exp, obs *fiscal.Money
		var accounts []subledger.Account
		if mapped {
			// A mapped account with no subledger activity and no ERP figure
			// compares nothing.
			if e.net.IsZero() && !isReported && !postedOn(balances, e.accounts) {
				continue
			}
			n := e.net
			exp = &n
			accounts = slices.Clone(e.accounts)
			slices.Sort(accounts)
		}
		if isReported {
			n, err := r.Net()
			if err != nil {
				return GLReconciliation{}, err
			}
			obs = &n
		}
		it, err := reconciliation.Compare(reconciliation.R6SubledgerToGL, reconciliation.MatchKey("gl:"+gl), exp, obs, none, false)
		if err != nil {
			return GLReconciliation{}, err
		}
		switch {
		case !mapped:
			it.Detail = "reported by the ERP; no TCSL account maps to this GL account"
		case !isReported:
			it.Detail = "the ERP reported no balance for this mapped GL account"
		}
		out.Items = append(out.Items, GLItem{Item: it, GLAccount: gl, TCSLAccounts: accounts})
	}
	slices.Sort(unmapped)
	for _, a := range unmapped {
		k := subledger.BalanceKey{Account: a, Currency: cur}
		net, err := balances[k].Debits.Sub(balances[k].Credits)
		if err != nil {
			return GLReconciliation{}, err
		}
		it, err := reconciliation.Compare(reconciliation.R6SubledgerToGL, reconciliation.MatchKey("tcsl:"+string(a)), &net, nil, none, false)
		if err != nil {
			return GLReconciliation{}, err
		}
		it.Detail = "no GL account is mapped for this TCSL account; its export is held"
		out.Items = append(out.Items, GLItem{Item: it, TCSLAccounts: []subledger.Account{a}})
	}

	out.Outcome = OutcomeReconciled
	for _, it := range out.Items {
		if it.Status.Exception() {
			out.Outcome = OutcomeVariances
			break
		}
	}
	if len(out.Holds) > 0 {
		out.Outcome = OutcomeHeld
	}
	return out, nil
}

// postedOn reports whether any of the accounts has posted lines, even ones
// that net to zero.
func postedOn(balances map[subledger.BalanceKey]subledger.Balance, accounts []subledger.Account) bool {
	for k := range balances {
		if slices.Contains(accounts, k.Account) {
			return true
		}
	}
	return false
}

// Exceptions counts the items that did not tie.
func (r GLReconciliation) Exceptions() int {
	n := 0
	for _, it := range r.Items {
		if it.Status.Exception() {
			n++
		}
	}
	return n
}
