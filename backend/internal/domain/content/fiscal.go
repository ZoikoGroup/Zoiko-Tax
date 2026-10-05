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
	Accumulators []AccumulatorBinding `json:"accumulators,omitempty"`
	Posting      *PostingDeclaration  `json:"posting,omitempty"`
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
	out.Accumulators = append([]AccumulatorBinding(nil), f.Accumulators...)
	sort.Slice(out.Accumulators, func(i, j int) bool { return out.Accumulators[i].Read < out.Accumulators[j].Read })
	for i := range out.Accumulators {
		ts := append([]ThresholdDeclaration(nil), out.Accumulators[i].Thresholds...)
		sort.Slice(ts, func(a, b int) bool { return ts[a].ID < ts[b].ID })
		out.Accumulators[i].Thresholds = ts
	}
	return out
}
