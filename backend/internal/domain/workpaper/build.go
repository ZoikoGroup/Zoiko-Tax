package workpaper

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/contribution"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// FormulaVersion names the assembly formula in this file. It is part of every
// build's inputs, so a change to the formula changes the inputs hash: the
// next build of an approved workpaper revokes its approval, and an approval
// of a build made under another formula is refused (OBL-07 "Formula
// changed").
const FormulaVersion = "obl07-assembly/v1"

// Sources are the records a build reads. Each is already a snapshot the
// caller took inside one transaction; Assemble reads nothing else.
type Sources struct {
	// Contributions are every provider contribution of the period, in any
	// status, for any entity or regime. Assemble keeps the workpaper's own.
	Contributions []contribution.Contribution
	// KnownOriginals reports which reversed source events the ledger holds,
	// in any period. A reversal whose original is unknown leaves its group
	// DATA_INCOMPLETE.
	KnownOriginals map[contribution.SourceKey]bool
	// Subledger is the legal filer's Tax Control Subledger balances posted
	// into the period, by account and currency.
	Subledger []subledger.ManifestBalance
}

// Exception codes. Every exception blocks READY_FOR_REVIEW until it is gone
// from a later build or, where it is resolvable, resolved by two people.
const (
	// ExceptionSourceMissing is a workpaper with no source event at all for
	// its filer, duty and period (OBL-07 "Source missing"). Resolvable: a
	// confirmed nil period is a resolution, with its evidence.
	ExceptionSourceMissing = "SOURCE_MISSING"
	// ExceptionSourceIncomplete is a held import or an unbalanced reversal in
	// the contribution ledger. It is repaired at the source, never here.
	ExceptionSourceIncomplete = "SOURCE_INCOMPLETE"
	// ExceptionCurrencyMismatch is source revenue in a currency other than
	// the reporting currency with no documented FX rate and conversion basis.
	// It is cleared by documenting one and rebuilding, never by a resolution.
	ExceptionCurrencyMismatch = "CURRENCY_MISMATCH"
	// ExceptionControlDifference is a schedule total that does not equal the
	// independently computed control total of the same events.
	ExceptionControlDifference = "CONTROL_TOTAL_DIFFERENCE"
	// ExceptionSuspenseOpen is an open suspense balance in the filer's
	// subledger for the period.
	ExceptionSuspenseOpen = "SUBLEDGER_SUSPENSE_OPEN"
	// ExceptionGLBridgeDifference is a customer GL bridge balance that does
	// not clear in the period.
	ExceptionGLBridgeDifference = "GL_BRIDGE_DIFFERENCE"
)

// Reconciliation line statuses.
const (
	ReconMatched    = "MATCHED"
	ReconDifference = "DIFFERENCE"
	ReconBridged    = "BRIDGED"
)

// Reconciliation line codes.
const (
	ReconEligibleVsControl = "SCHEDULE_ELIGIBLE_VS_CONTROL_TOTAL"
	ReconExcludedVsControl = "SCHEDULE_EXCLUDED_VS_CONTROL_TOTAL"
	ReconReportableBridge  = "REPORTABLE_BASE_BRIDGE"
	ReconGLBridge          = "SUBLEDGER_GL_BRIDGE"
	ReconSuspense          = "SUBLEDGER_SUSPENSE"
)

// Output is a build's sealed document: the schedules, the control totals and
// the reconciliation. Every amount in it is the canonical decimal string the
// builder computed with fiscal.Money; nothing reads one back as a number
// except a re-verification of the document's digest.
type Output struct {
	FormulaVersion    string           `json:"formulaVersion"`
	ContentRelease    string           `json:"contentRelease"`
	ReportingCurrency string           `json:"reportingCurrency"`
	Revenue           []RevenueLine    `json:"revenue"`
	Excluded          []ExclusionLine  `json:"excluded"`
	Volumes           Volumes          `json:"volumes"`
	Allowances        []AdjustmentLine `json:"allowances"`
	Adjustments       []AdjustmentLine `json:"adjustments"`
	// Totals are in the reporting currency. Nil while a source currency has
	// no documented conversion: an unconverted amount is never added to a
	// converted one.
	Totals         *Totals         `json:"totals,omitempty"`
	ControlTotals  []ControlTotal  `json:"controlTotals"`
	Reconciliation []ReconLine     `json:"reconciliation"`
	Subledger      []BalanceLine   `json:"subledger"`
	Exceptions     []Exception     `json:"exceptions"`
	Conversions    []ConversionUse `json:"conversions"`
}

// RevenueLine is one revenue category: the eligible base of the accepted
// events classified to it, in their one source currency, with the events
// themselves for drill-down to the source.
type RevenueLine struct {
	Category  string      `json:"category"`
	Currency  string      `json:"currency"`
	Count     int         `json:"count"`
	Amount    string      `json:"amount"`
	Converted string      `json:"converted,omitempty"`
	Sources   []SourceRef `json:"sources"`
}

// ExclusionLine is one excluded base: revenue outside the contribution base,
// by the reason the rule gave.
type ExclusionLine struct {
	Reason    string      `json:"reason"`
	Currency  string      `json:"currency"`
	Count     int         `json:"count"`
	Amount    string      `json:"amount"`
	Converted string      `json:"converted,omitempty"`
	Sources   []SourceRef `json:"sources"`
}

// SourceRef is the lineage of a schedule line back to one source event.
type SourceRef struct {
	ContributionID string `json:"contributionId"`
	SourceEventID  string `json:"sourceEventId"`
	SourceVersion  int    `json:"sourceVersion"`
	SourceRef      string `json:"sourceRef"`
	RuleVersion    string `json:"ruleVersion"`
	Amount         string `json:"amount"`
	Reverses       string `json:"reverses,omitempty"`
}

// Volumes are the transaction and volume counts.
type Volumes struct {
	Accepted    int              `json:"accepted"`
	Quarantined int              `json:"quarantined"`
	Reversals   int              `json:"reversals"`
	Amendments  int              `json:"amendments"`
	Quantities  []QuantityLine   `json:"quantities"`
	BySource    []SourceTypeLine `json:"bySource"`
}

// QuantityLine is a total quantity in one unit.
type QuantityLine struct {
	Unit  string `json:"unit"`
	Total string `json:"total"`
}

// SourceTypeLine is the BSS control total of one kind of source record in
// one currency.
type SourceTypeLine struct {
	SourceType string `json:"sourceType"`
	Currency   string `json:"currency"`
	Count      int    `json:"count"`
	Eligible   string `json:"eligible"`
}

// AdjustmentLine is an approved, evidence-linked allowance or adjustment as
// it entered the build, in the reporting currency.
type AdjustmentLine struct {
	Number         int    `json:"number"`
	Category       string `json:"category"`
	Amount         string `json:"amount"`
	Reason         string `json:"reason"`
	EvidenceSHA256 string `json:"evidenceSha256"`
	Material       bool   `json:"material"`
}

// Totals are the report totals in the reporting currency.
type Totals struct {
	Currency       string `json:"currency"`
	GrossEligible  string `json:"grossEligible"`
	Excluded       string `json:"excluded"`
	Allowances     string `json:"allowances"`
	Adjustments    string `json:"adjustments"`
	ReportableBase string `json:"reportableBase"`
}

// ControlTotal is the contribution ledger's own control total for the
// filer, duty, period and one currency: the independent expectation the
// schedules are reconciled to.
type ControlTotal struct {
	Currency        string   `json:"currency"`
	Accepted        int      `json:"accepted"`
	Quarantined     int      `json:"quarantined"`
	Reversals       int      `json:"reversals"`
	Eligible        string   `json:"eligible"`
	Excluded        string   `json:"excluded"`
	PeriodDigest    string   `json:"periodDigest"`
	Incomplete      []string `json:"incomplete"`
	SourceChecksums []string `json:"sourceChecksums"`
}

// ReconLine is one reconciliation: an expected and an actual amount in one
// currency, their difference, and whether they tie.
type ReconLine struct {
	Code       string `json:"code"`
	Currency   string `json:"currency"`
	Expected   string `json:"expected"`
	Actual     string `json:"actual"`
	Difference string `json:"difference"`
	Status     string `json:"status"`
}

// BalanceLine is one subledger control balance, debits and credits apart.
type BalanceLine struct {
	Account  string `json:"account"`
	Currency string `json:"currency"`
	Debits   string `json:"debits"`
	Credits  string `json:"credits"`
}

// Exception is one reconciliation exception. Key is stable across builds;
// Fingerprint also covers the amount, so a resolution of one difference does
// not carry over to a different one.
type Exception struct {
	Key         string `json:"key"`
	Code        string `json:"code"`
	Currency    string `json:"currency,omitempty"`
	Difference  string `json:"difference,omitempty"`
	Detail      string `json:"detail"`
	Resolvable  bool   `json:"resolvable"`
	Fingerprint string `json:"fingerprint"`
}

// ConversionUse records a documented FX rate a build converted with.
type ConversionUse struct {
	From         string `json:"from"`
	To           string `json:"to"`
	Rate         string `json:"rate"`
	RateSource   string `json:"rateSource"`
	RateDate     string `json:"rateDate"`
	RoundingMode string `json:"roundingMode"`
	Scale        int    `json:"scale"`
	Basis        string `json:"basis"`
}

// Assembly is what one build produced: the document, its digest, the digest
// of everything that went into it, and the digest of the preparer-controlled
// part of those inputs.
type Assembly struct {
	Output      Output
	OutputHash  canonical.Digest
	InputsHash  canonical.Digest
	BasisDigest canonical.Digest
}

// Assemble builds a workpaper's schedules and reconciliation from its
// sources. It is a pure function of the workpaper's header and basis — its
// content release, documented FX rates and approved adjustments — and the
// sources: the same inputs give the same output and the same hashes, which
// is what makes a build idempotent and a frozen build checkable.
func Assemble(w Workpaper, src Sources) (Assembly, error) {
	rep := w.ReportingCurrency
	var own []contribution.Contribution
	for _, c := range src.Contributions {
		if c.LegalEntity == w.LegalEntity && c.Regime == w.Duty && c.Period == w.Period {
			own = append(own, c)
		}
	}
	slices.SortFunc(own, func(a, b contribution.Contribution) int { return strings.Compare(a.ID.String(), b.ID.String()) })
	known := func(k contribution.SourceKey) bool { return src.KnownOriginals[k] }
	controls, err := contribution.Summarize(w.Period, own, known)
	if err != nil {
		return Assembly{}, err
	}

	b := builder{rep: rep, fx: map[fiscal.Currency]FXRate{}}
	for _, r := range w.CurrentFX() {
		b.fx[r.Currency] = r
	}
	out := Output{
		FormulaVersion: FormulaVersion, ContentRelease: w.ContentRelease, ReportingCurrency: string(rep),
		Revenue: []RevenueLine{}, Excluded: []ExclusionLine{}, Allowances: []AdjustmentLine{}, Adjustments: []AdjustmentLine{},
		ControlTotals: []ControlTotal{}, Reconciliation: []ReconLine{}, Subledger: []BalanceLine{}, Exceptions: []Exception{},
		Conversions: []ConversionUse{},
		Volumes:     Volumes{Quantities: []QuantityLine{}, BySource: []SourceTypeLine{}},
	}
	if err := b.schedules(&out, own); err != nil {
		return Assembly{}, err
	}
	if len(own) == 0 {
		b.exception(ExceptionSourceMissing, "", "", true,
			fmt.Sprintf("No source event is recorded for this filer, %s and %s. Material data is absent; a nil period needs evidence and a second person's confirmation.", w.Duty, w.Period))
	}
	if err := b.controls(&out, controls); err != nil {
		return Assembly{}, err
	}
	if err := b.totals(&out, w); err != nil {
		return Assembly{}, err
	}
	if err := b.subledger(&out, src.Subledger); err != nil {
		return Assembly{}, err
	}
	for _, cur := range b.currencies() {
		r, ok := b.fx[cur]
		if !ok {
			continue
		}
		out.Conversions = append(out.Conversions, ConversionUse{From: string(cur), To: string(rep), Rate: r.Rate.CanonicalString(),
			RateSource: r.Source, RateDate: r.RateDate.Format("2006-01-02"), RoundingMode: string(r.RoundingMode),
			Scale: int(r.Scale), Basis: r.Basis})
	}
	sort.SliceStable(b.exceptions, func(i, j int) bool { return b.exceptions[i].Key < b.exceptions[j].Key })
	out.Exceptions = b.exceptions

	outHash, err := canonical.Sum(out.Canonical())
	if err != nil {
		return Assembly{}, err
	}
	basis, err := w.BasisDigest()
	if err != nil {
		return Assembly{}, err
	}
	inHash, err := canonical.Sum(inputs(w, own, src, basis))
	if err != nil {
		return Assembly{}, err
	}
	return Assembly{Output: out, OutputHash: outHash, InputsHash: inHash, BasisDigest: basis}, nil
}

// inputs is the canonical record of everything a build read.
func inputs(w Workpaper, own []contribution.Contribution, src Sources, basis canonical.Digest) canonical.Value {
	events := make([]canonical.Value, len(own))
	for i, c := range own {
		reverses := canonical.Absent()
		if c.Reverses != nil {
			reverses = canonical.Bool(src.KnownOriginals[*c.Reverses])
		}
		events[i] = canonical.Object(
			canonical.F("contributionId", canonical.String(c.ID.String())),
			canonical.F("immutableHash", canonical.String(c.Hash.String())),
			canonical.F("status", canonical.String(string(c.Status))),
			canonical.F("originalKnown", reverses),
		)
	}
	balances := slices.Clone(src.Subledger)
	slices.SortFunc(balances, func(a, b subledger.ManifestBalance) int {
		if c := strings.Compare(string(a.Account), string(b.Account)); c != 0 {
			return c
		}
		return strings.Compare(string(a.Currency), string(b.Currency))
	})
	lines := make([]canonical.Value, len(balances))
	for i, bal := range balances {
		lines[i] = canonical.Object(
			canonical.F("account", canonical.String(string(bal.Account))),
			canonical.F("currency", canonical.String(string(bal.Currency))),
			canonical.F("debits", canonical.Money(bal.Debits)),
			canonical.F("credits", canonical.Money(bal.Credits)),
		)
	}
	return canonical.Object(
		canonical.F("formulaVersion", canonical.String(FormulaVersion)),
		canonical.F("legalEntityId", canonical.String(w.LegalEntity.String())),
		canonical.F("duty", canonical.String(string(w.Duty))),
		canonical.F("period", canonical.String(w.Period)),
		canonical.F("reportingCurrency", canonical.String(string(w.ReportingCurrency))),
		canonical.F("materiality", canonical.Money(w.Materiality)),
		canonical.F("basisDigest", canonical.String(basis.String())),
		canonical.F("contributions", canonical.Array(events...)),
		canonical.F("subledger", canonical.Array(lines...)),
	)
}

type builder struct {
	rep        fiscal.Currency
	fx         map[fiscal.Currency]FXRate
	seen       map[fiscal.Currency]bool
	missingFX  map[fiscal.Currency]bool
	revenue    map[fiscal.Currency]fiscal.Money
	excluded   map[fiscal.Currency]fiscal.Money
	exceptions []Exception
}

func (b *builder) note(cur fiscal.Currency) {
	if b.seen == nil {
		b.seen = map[fiscal.Currency]bool{}
	}
	b.seen[cur] = true
}

func (b *builder) currencies() []fiscal.Currency {
	out := make([]fiscal.Currency, 0, len(b.seen))
	for c := range b.seen {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

func (b *builder) exception(code, currency, difference string, resolvable bool, detail string) {
	key := code
	if currency != "" {
		key += "/" + currency
	}
	e := Exception{Key: key, Code: code, Currency: currency, Difference: difference, Detail: detail, Resolvable: resolvable}
	d, err := canonical.Sum(canonical.Object(
		canonical.F("key", canonical.String(key)),
		canonical.F("code", canonical.String(code)),
		canonical.F("currency", canonical.OptString(currency)),
		canonical.F("difference", canonical.OptString(difference)),
		canonical.F("detail", canonical.String(detail)),
	))
	if err == nil {
		e.Fingerprint = d.String()
	}
	b.exceptions = append(b.exceptions, e)
}

// convert converts a source-currency amount into the reporting currency at
// the documented rate, or reports that it cannot.
func (b *builder) convert(m fiscal.Money) (fiscal.Money, bool, error) {
	cur := m.Currency()
	if cur == b.rep {
		return m, true, nil
	}
	r, ok := b.fx[cur]
	if !ok {
		if b.missingFX == nil {
			b.missingFX = map[fiscal.Currency]bool{}
		}
		b.missingFX[cur] = true
		return fiscal.Money{}, false, nil
	}
	p, err := r.Policy()
	if err != nil {
		return fiscal.Money{}, false, err
	}
	out, err := m.ConvertAt(r.Rate, b.rep, p)
	if err != nil {
		return fiscal.Money{}, false, err
	}
	return out, true, nil
}

func zero(cur fiscal.Currency) (fiscal.Money, error) { return fiscal.ParseMoney("0", cur) }

func add(into map[fiscal.Currency]fiscal.Money, m fiscal.Money) error {
	cur, ok := into[m.Currency()]
	if !ok {
		into[m.Currency()] = m
		return nil
	}
	sum, err := cur.Add(m)
	if err != nil {
		return err
	}
	into[m.Currency()] = sum
	return nil
}

func ref(c contribution.Contribution, amount fiscal.Money) SourceRef {
	r := SourceRef{ContributionID: c.ID.String(), SourceEventID: c.Source.EventID, SourceVersion: c.Source.Version,
		SourceRef: c.SourceRef, RuleVersion: c.RuleVersion, Amount: amount.CanonicalString()}
	if c.Reverses != nil {
		r.Reverses = c.Reverses.String()
	}
	return r
}

// schedules computes the revenue categories, the excluded bases and the
// volumes from the accepted events, line by line. It does not use the
// contribution ledger's totals: those are the independent expectation the
// lines are reconciled to.
func (b *builder) schedules(out *Output, own []contribution.Contribution) error {
	type key struct{ label, cur string }
	type acc struct {
		count   int
		amount  fiscal.Money
		sources []SourceRef
	}
	revenue, excluded := map[key]*acc{}, map[key]*acc{}
	quantities := map[string]fiscal.Quantity{}
	bySource := map[key]*acc{}
	b.revenue, b.excluded = map[fiscal.Currency]fiscal.Money{}, map[fiscal.Currency]fiscal.Money{}
	put := func(m map[key]*acc, k key, amount fiscal.Money, src *SourceRef) error {
		a, ok := m[k]
		if !ok {
			z, err := zero(amount.Currency())
			if err != nil {
				return err
			}
			a = &acc{amount: z}
			m[k] = a
		}
		sum, err := a.amount.Add(amount)
		if err != nil {
			return err
		}
		a.amount = sum
		a.count++
		if src != nil {
			a.sources = append(a.sources, *src)
		}
		return nil
	}
	for _, c := range own {
		if c.Status != contribution.StatusAccepted {
			out.Volumes.Quarantined++
			continue
		}
		cur := c.Currency()
		b.note(cur)
		out.Volumes.Accepted++
		if c.IsReversal() {
			out.Volumes.Reversals++
		}
		if c.Amendment {
			out.Volumes.Amendments++
		}
		r := ref(c, c.EligibleBase)
		if err := put(revenue, key{c.ClassificationRef, string(cur)}, c.EligibleBase, &r); err != nil {
			return err
		}
		if err := add(b.revenue, c.EligibleBase); err != nil {
			return err
		}
		if !c.ExcludedBase.IsZero() {
			x := ref(c, c.ExcludedBase)
			if err := put(excluded, key{c.ExclusionReason, string(cur)}, c.ExcludedBase, &x); err != nil {
				return err
			}
			if err := add(b.excluded, c.ExcludedBase); err != nil {
				return err
			}
		}
		if err := put(bySource, key{string(c.SourceType), string(cur)}, c.EligibleBase, nil); err != nil {
			return err
		}
		if c.Quantity != nil {
			u := string(c.Quantity.Unit())
			if q, ok := quantities[u]; ok {
				sum, err := q.Add(*c.Quantity)
				if err != nil {
					return err
				}
				quantities[u] = sum
			} else {
				quantities[u] = *c.Quantity
			}
		}
	}
	ordered := func(m map[key]*acc) []key {
		keys := make([]key, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(x, y key) int {
			if c := strings.Compare(x.label, y.label); c != 0 {
				return c
			}
			return strings.Compare(x.cur, y.cur)
		})
		return keys
	}
	sortRefs := func(refs []SourceRef) []SourceRef {
		slices.SortFunc(refs, func(x, y SourceRef) int {
			if c := strings.Compare(x.SourceEventID, y.SourceEventID); c != 0 {
				return c
			}
			return x.SourceVersion - y.SourceVersion
		})
		return refs
	}
	for _, k := range ordered(revenue) {
		a := revenue[k]
		line := RevenueLine{Category: k.label, Currency: k.cur, Count: a.count, Amount: a.amount.CanonicalString(), Sources: sortRefs(a.sources)}
		conv, ok, err := b.convert(a.amount)
		if err != nil {
			return err
		}
		if ok && k.cur != string(b.rep) {
			line.Converted = conv.CanonicalString()
		}
		out.Revenue = append(out.Revenue, line)
	}
	for _, k := range ordered(excluded) {
		a := excluded[k]
		line := ExclusionLine{Reason: k.label, Currency: k.cur, Count: a.count, Amount: a.amount.CanonicalString(), Sources: sortRefs(a.sources)}
		conv, ok, err := b.convert(a.amount)
		if err != nil {
			return err
		}
		if ok && k.cur != string(b.rep) {
			line.Converted = conv.CanonicalString()
		}
		out.Excluded = append(out.Excluded, line)
	}
	for _, k := range ordered(bySource) {
		a := bySource[k]
		out.Volumes.BySource = append(out.Volumes.BySource, SourceTypeLine{SourceType: k.label, Currency: k.cur, Count: a.count,
			Eligible: a.amount.CanonicalString()})
	}
	units := make([]string, 0, len(quantities))
	for u := range quantities {
		units = append(units, u)
	}
	slices.Sort(units)
	for _, u := range units {
		q := quantities[u]
		out.Volumes.Quantities = append(out.Volumes.Quantities, QuantityLine{Unit: u, Total: q.CanonicalString()})
	}
	for _, cur := range b.currencies() {
		if b.missingFX[cur] {
			b.exception(ExceptionCurrencyMismatch, string(cur), "", false,
				fmt.Sprintf("Source revenue is in %s and the reporting currency is %s. Document an FX rate and its conversion basis for %s, then rebuild.",
					cur, b.rep, cur))
		}
	}
	return nil
}

func difference(actual, expected fiscal.Money) (fiscal.Money, string, error) {
	d, err := actual.Sub(expected)
	if err != nil {
		return fiscal.Money{}, "", err
	}
	status := ReconMatched
	if !d.IsZero() {
		status = ReconDifference
	}
	return d, status, nil
}

// controls records the contribution ledger's control totals and reconciles
// the schedules to them, currency by currency.
func (b *builder) controls(out *Output, controls []contribution.Totals) error {
	for _, t := range controls {
		cur := t.Currency
		ct := ControlTotal{Currency: string(cur), Accepted: t.Accepted, Quarantined: t.Quarantined, Reversals: t.Reversals,
			Eligible: t.Eligible.CanonicalString(), Excluded: t.Excluded.CanonicalString(), PeriodDigest: t.Digest.String(),
			Incomplete: []string{}, SourceChecksums: append([]string{}, t.SourceChecksums...)}
		for _, r := range t.Incomplete {
			ct.Incomplete = append(ct.Incomplete, string(r))
		}
		out.ControlTotals = append(out.ControlTotals, ct)
		if t.DataIncomplete() {
			b.exception(ExceptionSourceIncomplete, string(cur), "", false,
				fmt.Sprintf("The contribution ledger is DATA_INCOMPLETE for %s (%s). Release or repair the held events at the source, then rebuild.",
					cur, strings.Join(ct.Incomplete, ", ")))
		}
		for _, pair := range []struct {
			code     string
			expected fiscal.Money
			lines    map[fiscal.Currency]fiscal.Money
		}{
			{ReconEligibleVsControl, t.Eligible, b.revenue},
			{ReconExcludedVsControl, t.Excluded, b.excluded},
		} {
			actual, have := pair.lines[cur]
			if !have {
				z, err := zero(cur)
				if err != nil {
					return err
				}
				actual = z
			}
			d, status, err := difference(actual, pair.expected)
			if err != nil {
				return err
			}
			out.Reconciliation = append(out.Reconciliation, ReconLine{Code: pair.code, Currency: string(cur),
				Expected: pair.expected.CanonicalString(), Actual: actual.CanonicalString(), Difference: d.CanonicalString(), Status: status})
			if status == ReconDifference {
				b.exception(ExceptionControlDifference, string(cur)+"/"+pair.code, d.CanonicalString(), true,
					fmt.Sprintf("%s: the schedule says %s and the contribution ledger's control total says %s %s.",
						pair.code, actual.CanonicalString(), pair.expected.CanonicalString(), cur))
			}
		}
	}
	return nil
}

// totals converts the schedules into the reporting currency and bridges the
// gross eligible base to the reportable base through the approved
// allowances and adjustments.
func (b *builder) totals(out *Output, w Workpaper) error {
	rep := b.rep
	allowances, adjustments, err := zeroPair(rep)
	if err != nil {
		return err
	}
	for _, a := range w.Adjustments {
		if a.State != AdjustmentApproved {
			continue
		}
		line := AdjustmentLine{Number: a.Number, Category: a.Category, Amount: a.Amount.CanonicalString(), Reason: a.Reason,
			EvidenceSHA256: a.Evidence.SHA256, Material: a.Material}
		if a.Kind == KindAllowance {
			out.Allowances = append(out.Allowances, line)
			if allowances, err = allowances.Add(a.Amount); err != nil {
				return err
			}
		} else {
			out.Adjustments = append(out.Adjustments, line)
			if adjustments, err = adjustments.Add(a.Amount); err != nil {
				return err
			}
		}
	}
	if len(b.missingFX) > 0 {
		return nil
	}
	gross, excluded, err := zeroPair(rep)
	if err != nil {
		return err
	}
	for _, cur := range b.currencies() {
		for _, pair := range []struct {
			into *fiscal.Money
			from map[fiscal.Currency]fiscal.Money
		}{{&gross, b.revenue}, {&excluded, b.excluded}} {
			m, ok := pair.from[cur]
			if !ok {
				continue
			}
			conv, _, err := b.convert(m)
			if err != nil {
				return err
			}
			if *pair.into, err = pair.into.Add(conv); err != nil {
				return err
			}
		}
	}
	reportable, err := gross.Sub(allowances)
	if err != nil {
		return err
	}
	if reportable, err = reportable.Add(adjustments); err != nil {
		return err
	}
	out.Totals = &Totals{Currency: string(rep), GrossEligible: gross.CanonicalString(), Excluded: excluded.CanonicalString(),
		Allowances: allowances.CanonicalString(), Adjustments: adjustments.CanonicalString(), ReportableBase: reportable.CanonicalString()}
	d, err := reportable.Sub(gross)
	if err != nil {
		return err
	}
	status := ReconMatched
	if !d.IsZero() {
		// The difference is exactly the approved, evidence-linked
		// allowances and adjustments listed beside it.
		status = ReconBridged
	}
	out.Reconciliation = append(out.Reconciliation, ReconLine{Code: ReconReportableBridge, Currency: string(rep),
		Expected: gross.CanonicalString(), Actual: reportable.CanonicalString(), Difference: d.CanonicalString(), Status: status})
	return nil
}

func zeroPair(cur fiscal.Currency) (fiscal.Money, fiscal.Money, error) {
	z, err := zero(cur)
	return z, z, err
}

// subledger lists the filer's control balances for the period and checks the
// two that must clear: suspense and the customer GL bridge. No equality is
// asserted between a provider contribution and retail tax collected; the
// subledger holds the latter and is shown beside the schedules, not netted
// into them (LED-19).
func (b *builder) subledger(out *Output, balances []subledger.ManifestBalance) error {
	sorted := slices.Clone(balances)
	slices.SortFunc(sorted, func(x, y subledger.ManifestBalance) int {
		if c := strings.Compare(string(x.Account), string(y.Account)); c != 0 {
			return c
		}
		return strings.Compare(string(x.Currency), string(y.Currency))
	})
	for _, bal := range sorted {
		out.Subledger = append(out.Subledger, BalanceLine{Account: string(bal.Account), Currency: string(bal.Currency),
			Debits: bal.Debits.CanonicalString(), Credits: bal.Credits.CanonicalString()})
		var code, exception string
		switch bal.Account {
		case subledger.AccountSuspenseException:
			code, exception = ReconSuspense, ExceptionSuspenseOpen
		case subledger.AccountCustomerGLBridge:
			code, exception = ReconGLBridge, ExceptionGLBridgeDifference
		default:
			continue
		}
		d, status, err := difference(bal.Credits, bal.Debits)
		if err != nil {
			return err
		}
		out.Reconciliation = append(out.Reconciliation, ReconLine{Code: code, Currency: string(bal.Currency),
			Expected: bal.Debits.CanonicalString(), Actual: bal.Credits.CanonicalString(), Difference: d.CanonicalString(), Status: status})
		if status == ReconDifference {
			b.exception(exception, string(bal.Currency), d.CanonicalString(), true,
				fmt.Sprintf("%s does not clear in the period: debits %s, credits %s %s.", bal.Account,
					bal.Debits.CanonicalString(), bal.Credits.CanonicalString(), bal.Currency))
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// the sealed document
// ---------------------------------------------------------------------------

func refsValue(refs []SourceRef) canonical.Value {
	items := make([]canonical.Value, len(refs))
	for i, r := range refs {
		items[i] = canonical.Object(
			canonical.F("contributionId", canonical.String(r.ContributionID)),
			canonical.F("sourceEventId", canonical.String(r.SourceEventID)),
			canonical.F("sourceVersion", canonical.Integer(int64(r.SourceVersion))),
			canonical.F("sourceRef", canonical.String(r.SourceRef)),
			canonical.F("ruleVersion", canonical.String(r.RuleVersion)),
			canonical.F("amount", canonical.String(r.Amount)),
			canonical.F("reverses", canonical.OptString(r.Reverses)),
		)
	}
	return canonical.Array(items...)
}

func strings2(items []string) canonical.Value {
	out := make([]canonical.Value, len(items))
	for i, s := range items {
		out[i] = canonical.String(s)
	}
	return canonical.Array(out...)
}

func each[T any](items []T, fn func(T) canonical.Value) canonical.Value {
	out := make([]canonical.Value, len(items))
	for i, it := range items {
		out[i] = fn(it)
	}
	return canonical.Array(out...)
}

func integer(n int) canonical.Value { return canonical.Integer(int64(n)) }

// Canonical is the document the output hash covers. It is the document that
// is stored: Encode of this value is what the store keeps, and DecodeOutput
// reads it back and checks it against the hash.
func (o Output) Canonical() canonical.Value {
	str, opt := canonical.String, canonical.OptString
	totals := canonical.Absent()
	if o.Totals != nil {
		t := o.Totals
		totals = canonical.Object(
			canonical.F("currency", str(t.Currency)),
			canonical.F("grossEligible", str(t.GrossEligible)),
			canonical.F("excluded", str(t.Excluded)),
			canonical.F("allowances", str(t.Allowances)),
			canonical.F("adjustments", str(t.Adjustments)),
			canonical.F("reportableBase", str(t.ReportableBase)),
		)
	}
	adjustment := func(a AdjustmentLine) canonical.Value {
		return canonical.Object(
			canonical.F("number", integer(a.Number)),
			canonical.F("category", str(a.Category)),
			canonical.F("amount", str(a.Amount)),
			canonical.F("reason", str(a.Reason)),
			canonical.F("evidenceSha256", str(a.EvidenceSHA256)),
			canonical.F("material", canonical.Bool(a.Material)),
		)
	}
	return canonical.Object(
		canonical.F("formulaVersion", str(o.FormulaVersion)),
		canonical.F("contentRelease", str(o.ContentRelease)),
		canonical.F("reportingCurrency", str(o.ReportingCurrency)),
		canonical.F("revenue", each(o.Revenue, func(l RevenueLine) canonical.Value {
			return canonical.Object(
				canonical.F("category", str(l.Category)),
				canonical.F("currency", str(l.Currency)),
				canonical.F("count", integer(l.Count)),
				canonical.F("amount", str(l.Amount)),
				canonical.F("converted", opt(l.Converted)),
				canonical.F("sources", refsValue(l.Sources)),
			)
		})),
		canonical.F("excluded", each(o.Excluded, func(l ExclusionLine) canonical.Value {
			return canonical.Object(
				canonical.F("reason", str(l.Reason)),
				canonical.F("currency", str(l.Currency)),
				canonical.F("count", integer(l.Count)),
				canonical.F("amount", str(l.Amount)),
				canonical.F("converted", opt(l.Converted)),
				canonical.F("sources", refsValue(l.Sources)),
			)
		})),
		canonical.F("volumes", canonical.Object(
			canonical.F("accepted", integer(o.Volumes.Accepted)),
			canonical.F("quarantined", integer(o.Volumes.Quarantined)),
			canonical.F("reversals", integer(o.Volumes.Reversals)),
			canonical.F("amendments", integer(o.Volumes.Amendments)),
			canonical.F("quantities", each(o.Volumes.Quantities, func(q QuantityLine) canonical.Value {
				return canonical.Object(canonical.F("unit", str(q.Unit)), canonical.F("total", str(q.Total)))
			})),
			canonical.F("bySource", each(o.Volumes.BySource, func(s SourceTypeLine) canonical.Value {
				return canonical.Object(
					canonical.F("sourceType", str(s.SourceType)),
					canonical.F("currency", str(s.Currency)),
					canonical.F("count", integer(s.Count)),
					canonical.F("eligible", str(s.Eligible)),
				)
			})),
		)),
		canonical.F("allowances", each(o.Allowances, adjustment)),
		canonical.F("adjustments", each(o.Adjustments, adjustment)),
		canonical.F("totals", totals),
		canonical.F("controlTotals", each(o.ControlTotals, func(c ControlTotal) canonical.Value {
			return canonical.Object(
				canonical.F("currency", str(c.Currency)),
				canonical.F("accepted", integer(c.Accepted)),
				canonical.F("quarantined", integer(c.Quarantined)),
				canonical.F("reversals", integer(c.Reversals)),
				canonical.F("eligible", str(c.Eligible)),
				canonical.F("excluded", str(c.Excluded)),
				canonical.F("periodDigest", str(c.PeriodDigest)),
				canonical.F("incomplete", strings2(c.Incomplete)),
				canonical.F("sourceChecksums", strings2(c.SourceChecksums)),
			)
		})),
		canonical.F("reconciliation", each(o.Reconciliation, func(r ReconLine) canonical.Value {
			return canonical.Object(
				canonical.F("code", str(r.Code)),
				canonical.F("currency", str(r.Currency)),
				canonical.F("expected", str(r.Expected)),
				canonical.F("actual", str(r.Actual)),
				canonical.F("difference", str(r.Difference)),
				canonical.F("status", str(r.Status)),
			)
		})),
		canonical.F("subledger", each(o.Subledger, func(l BalanceLine) canonical.Value {
			return canonical.Object(
				canonical.F("account", str(l.Account)),
				canonical.F("currency", str(l.Currency)),
				canonical.F("debits", str(l.Debits)),
				canonical.F("credits", str(l.Credits)),
			)
		})),
		canonical.F("exceptions", each(o.Exceptions, func(e Exception) canonical.Value {
			return canonical.Object(
				canonical.F("key", str(e.Key)),
				canonical.F("code", str(e.Code)),
				canonical.F("currency", opt(e.Currency)),
				canonical.F("difference", opt(e.Difference)),
				canonical.F("detail", str(e.Detail)),
				canonical.F("resolvable", canonical.Bool(e.Resolvable)),
				canonical.F("fingerprint", str(e.Fingerprint)),
			)
		})),
		canonical.F("conversions", each(o.Conversions, func(c ConversionUse) canonical.Value {
			return canonical.Object(
				canonical.F("from", str(c.From)),
				canonical.F("to", str(c.To)),
				canonical.F("rate", str(c.Rate)),
				canonical.F("rateSource", str(c.RateSource)),
				canonical.F("rateDate", str(c.RateDate)),
				canonical.F("roundingMode", str(c.RoundingMode)),
				canonical.F("scale", integer(c.Scale)),
				canonical.F("basis", str(c.Basis)),
			)
		})),
	)
}

// EncodeOutput renders the document in canonical form, for storage.
func EncodeOutput(o Output) ([]byte, error) { return canonical.Encode(o.Canonical()) }

// DecodeOutput reads a stored document and checks it against the hash it
// was sealed with. A document that does not re-hash to its digest is
// refused: the frozen file no longer matches the decision snapshot (LED-19
// "Compare workpaper digest").
func DecodeOutput(body []byte, want canonical.Digest) (Output, error) {
	var o Output
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return Output{}, fmt.Errorf("workpaper: stored output does not decode: %w", err)
	}
	got, err := canonical.Sum(o.Canonical())
	if err != nil {
		return Output{}, err
	}
	if !got.Equal(want) {
		return Output{}, fmt.Errorf("workpaper: stored output hashes to %s, sealed as %s", got, want)
	}
	return o, nil
}
