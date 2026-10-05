package content

import "sort"

// FiscalProfile is the part of a bundle that says what a committed decision
// does beyond being recorded: which running totals it contributes to, and how
// it posts to the Tax Control Subledger. It is content, signed with the rest of
// the manifest, because both are legal facts about a pack — over what period
// a threshold accumulates, what counts toward it, and which control accounts a
// tax amount moves — and none of them is Go's to decide (ZTAX-DET-001 §0.2).
//
// It is optional. A bundle without one is evaluated and recorded exactly as
// before, and decodes and digests to the same bytes it always did.
type FiscalProfile struct {
	Accumulators []AccumulatorBinding    `json:"accumulators,omitempty"`
	Posting      *PostingDeclaration     `json:"posting,omitempty"`
	Obligations  []ObligationDeclaration `json:"obligations,omitempty"`
}

// AccumulatorBinding connects an accumulator a rule reads to the store.
//
// Read is the key the rules name ("accumulator ytd \"threshold.ecoLevyYtd\"").
// The stored key adds the period, so a year-to-date total is one row per
// legal year rather than one row forever (ZTAX-DET-REQ-0025: tenant,
// threshold and period, and nothing else).
type AccumulatorBinding struct {
	Read     string `json:"read"`
	Currency string `json:"currency"`
	// Period is MONTH, QUARTER or YEAR, in Timezone's civil calendar.
	Period         string `json:"period"`
	Timezone       string `json:"timezone"`
	YearStartMonth int    `json:"yearStartMonth,omitempty"`
	// Contributes names the amount a committed decision adds: an input money
	// field or an emitted money slot. Exactly one.
	Contributes ContributionSource     `json:"contributes"`
	Thresholds  []ThresholdDeclaration `json:"thresholds,omitempty"`
}

// ContributionSource is where a contribution's amount comes from.
type ContributionSource struct {
	Input   string `json:"input,omitempty"`
	Emitted string `json:"emitted,omitempty"`
}

// ThresholdDeclaration is a limit on a bound accumulator, crossed exactly
// once (ADR-0004 §2.6).
type ThresholdDeclaration struct {
	ID         string `json:"id"`
	Limit      string `json:"limit"`
	Comparison string `json:"comparison"`
}

// PostingDeclaration is the content's posting profile for a committed
// decision: which emitted amounts move which control accounts
// (ZTAX-FIN-001 §12-§13).
type PostingDeclaration struct {
	Profile string                   `json:"profile"`
	Version string                   `json:"version"`
	Type    string                   `json:"type"`
	Lines   []PostingLineDeclaration `json:"lines"`
}

// PostingLineDeclaration is one line: an emitted money slot to an account,
// on a side.
type PostingLineDeclaration struct {
	Account string `json:"account"`
	Side    string `json:"side"`
	Emitted string `json:"emitted"`
}

// ObligationDeclaration is a periodic duty a committed decision is assessed
// into: a return for a period of the legal calendar, due on a date the
// content states, whose amount is the sum of the emitted slots it names over
// the decisions committed in that period (ZTAX-OBL-001 §4).
//
// A declaration is the subset of obligation.Definition this release
// evaluates: the trigger is the commit itself, the responsible party is the
// tenant's default legal entity, and the due date is the period end plus an
// offset in the civil calendar with no business-day adjustment, because no
// signed holiday calendar ships yet. A declaration that asks for more is
// refused rather than evaluated as something it did not say.
type ObligationDeclaration struct {
	ID           string `json:"id"`
	Version      string `json:"version"`
	Type         string `json:"type"`
	Authority    string `json:"authority"`
	Jurisdiction string `json:"jurisdiction"`
	// Duty is TRANSACTION_MONETARY: a transaction tax remitted per period.
	Duty     string `json:"duty"`
	Currency string `json:"currency"`
	// Assesses names the emitted money slots summed into the assessed amount.
	Assesses []string         `json:"assesses"`
	Period   ObligationPeriod `json:"period"`
	Due      ObligationDue    `json:"due"`
	Source   string           `json:"source"`
	Citation string           `json:"citation"`
}

// ObligationPeriod is the filing period, in Timezone's civil calendar.
type ObligationPeriod struct {
	Kind           string `json:"kind"`
	Timezone       string `json:"timezone"`
	YearStartMonth int    `json:"yearStartMonth,omitempty"`
}

// ObligationDue is the due date: the period's last day, moved by
// OffsetMonths, then pinned to DayOfMonth when it is set, then moved by
// OffsetDays.
type ObligationDue struct {
	OffsetMonths int `json:"offsetMonths,omitempty"`
	DayOfMonth   int `json:"dayOfMonth,omitempty"`
	OffsetDays   int `json:"offsetDays,omitempty"`
}

// Validate checks the profile is well-formed on its own. Whether the keys and
// slots it names exist is checked against the bundle's nodes when the bundle
// loads (rule.Load), because only the bundle knows them.
func (f FiscalProfile) Validate() error {
	seen := map[string]bool{}
	for i, a := range f.Accumulators {
		switch {
		case a.Read == "":
			return errorf("fiscal profile: accumulator binding %d names no key", i)
		case seen[a.Read]:
			return errorf("fiscal profile: accumulator %s is bound twice", a.Read)
		case a.Currency == "":
			return errorf("fiscal profile: accumulator %s names no currency", a.Read)
		case a.Timezone == "":
			return errorf("fiscal profile: accumulator %s names no legal timezone", a.Read)
		}
		seen[a.Read] = true
		switch a.Period {
		case "MONTH":
		case "QUARTER", "YEAR":
			if a.YearStartMonth < 1 || a.YearStartMonth > 12 {
				return errorf("fiscal profile: accumulator %s has year start month %d", a.Read, a.YearStartMonth)
			}
		default:
			return errorf("fiscal profile: accumulator %s has period %q; MONTH, QUARTER or YEAR", a.Read, a.Period)
		}
		if (a.Contributes.Input == "") == (a.Contributes.Emitted == "") {
			return errorf("fiscal profile: accumulator %s must name exactly one contribution source", a.Read)
		}
		ids := map[string]bool{}
		for _, t := range a.Thresholds {
			switch {
			case t.ID == "" || t.Limit == "":
				return errorf("fiscal profile: accumulator %s has a threshold with no id or no limit", a.Read)
			case ids[t.ID]:
				return errorf("fiscal profile: accumulator %s declares threshold %s twice", a.Read, t.ID)
			case t.Comparison != "GTE" && t.Comparison != "GT":
				return errorf("fiscal profile: threshold %s has comparison %q; GTE or GT", t.ID, t.Comparison)
			}
			ids[t.ID] = true
		}
	}
	obligations := map[string]bool{}
	for i, o := range f.Obligations {
		switch {
		case o.ID == "" || o.Version == "" || o.Type == "":
			return errorf("fiscal profile: obligation %d has no id, version or type", i)
		case obligations[o.ID]:
			return errorf("fiscal profile: obligation %s is declared twice", o.ID)
		case o.Authority == "" || o.Jurisdiction == "":
			return errorf("fiscal profile: obligation %s names no authority or no jurisdiction", o.ID)
		case o.Duty != "TRANSACTION_MONETARY":
			return errorf("fiscal profile: obligation %s has duty %q; this release assesses TRANSACTION_MONETARY", o.ID, o.Duty)
		case o.Currency == "" || len(o.Assesses) == 0:
			return errorf("fiscal profile: obligation %s names no currency or nothing it assesses", o.ID)
		case o.Source == "" || o.Citation == "":
			return errorf("fiscal profile: obligation %s carries no source or citation", o.ID)
		case o.Period.Timezone == "":
			return errorf("fiscal profile: obligation %s names no legal timezone", o.ID)
		case o.Due.DayOfMonth < 0 || o.Due.DayOfMonth > 31:
			return errorf("fiscal profile: obligation %s has due day of month %d", o.ID, o.Due.DayOfMonth)
		case o.Due.OffsetMonths < 0 || o.Due.OffsetDays < 0:
			return errorf("fiscal profile: obligation %s is due before its period ends", o.ID)
		}
		obligations[o.ID] = true
		switch o.Period.Kind {
		case "MONTH":
		case "QUARTER", "YEAR":
			if o.Period.YearStartMonth < 1 || o.Period.YearStartMonth > 12 {
				return errorf("fiscal profile: obligation %s has year start month %d", o.ID, o.Period.YearStartMonth)
			}
		default:
			return errorf("fiscal profile: obligation %s has period %q; MONTH, QUARTER or YEAR", o.ID, o.Period.Kind)
		}
		slots := map[string]bool{}
		for _, a := range o.Assesses {
			if a == "" || slots[a] {
				return errorf("fiscal profile: obligation %s assesses an empty or repeated slot", o.ID)
			}
			slots[a] = true
		}
	}
	if p := f.Posting; p != nil {
		if p.Profile == "" || p.Version == "" || p.Type == "" {
			return errorf("fiscal profile: posting names no profile, version or journal type")
		}
		if len(p.Lines) < 2 {
			return errorf("fiscal profile: posting declares %d lines; a balanced journal needs at least two", len(p.Lines))
		}
		for i, l := range p.Lines {
			if l.Account == "" || l.Emitted == "" || (l.Side != "DEBIT" && l.Side != "CREDIT") {
				return errorf("fiscal profile: posting line %d needs an account, a DEBIT or CREDIT side and an emitted slot", i)
			}
		}
	}
	return nil
}

// Normalize returns a copy with every unordered collection in key order, for
// canonical encoding. Posting lines keep their order: it is the journal's.
func (f FiscalProfile) Normalize() FiscalProfile {
	out := FiscalProfile{Posting: f.Posting}
	out.Obligations = append([]ObligationDeclaration(nil), f.Obligations...)
	sort.Slice(out.Obligations, func(i, j int) bool { return out.Obligations[i].ID < out.Obligations[j].ID })
	for i := range out.Obligations {
		out.Obligations[i].Assesses = append([]string(nil), out.Obligations[i].Assesses...)
		sort.Strings(out.Obligations[i].Assesses)
	}
	out.Accumulators = append([]AccumulatorBinding(nil), f.Accumulators...)
	sort.Slice(out.Accumulators, func(i, j int) bool { return out.Accumulators[i].Read < out.Accumulators[j].Read })
	for i := range out.Accumulators {
		ts := append([]ThresholdDeclaration(nil), out.Accumulators[i].Thresholds...)
		sort.Slice(ts, func(a, b int) bool { return ts[a].ID < ts[b].ID })
		out.Accumulators[i].Thresholds = ts
	}
	return out
}
