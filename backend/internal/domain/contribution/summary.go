package contribution

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// IncompleteReason says why a period's control totals are DATA_INCOMPLETE.
type IncompleteReason string

// The reasons.
const (
	// IncompleteQuarantined is a group with a quarantined event: an import
	// is held, so the totals do not yet say everything the sources said.
	IncompleteQuarantined IncompleteReason = "QUARANTINED_EVENTS"
	// IncompleteUnresolvedReversal is a reversal whose original source event
	// is not in the ledger: the source is unbalanced.
	IncompleteUnresolvedReversal IncompleteReason = "UNRESOLVED_REVERSAL"
)

// Group is what control totals are kept per: one member entity, one regime,
// one currency. Currencies are never added together.
type Group struct {
	LegalEntity id.LegalEntityID
	Regime      Regime
	Currency    fiscal.Currency
}

// Totals are one group's control totals for a period.
type Totals struct {
	Group
	Period string

	Accepted    int
	Quarantined int
	Reversals   int // accepted reversals, counted in Accepted too
	Amendments  int // accepted late events, counted in Accepted too

	// Eligible and Excluded are the sums over accepted events, reversals
	// included as the negatives they are. Quarantined events are listed
	// but never summed.
	Eligible fiscal.Money
	Excluded fiscal.Money
	// Reversed is the part of Eligible that reversals contributed: the
	// credit side, zero or negative.
	Reversed fiscal.Money

	// Digest is the period contribution digest: the canonical digest of the
	// group and the sorted immutable hashes of its accepted events. It does
	// not depend on the order the events arrived in.
	Digest canonical.Digest

	// SourceChecksums are the distinct import-file checksums the group's
	// events carried, sorted.
	SourceChecksums []string

	Incomplete []IncompleteReason
}

// DataIncomplete reports whether the totals are DATA_INCOMPLETE.
func (t Totals) DataIncomplete() bool { return len(t.Incomplete) > 0 }

// Summarize computes a period's control totals from the period's events.
// known reports whether a source event is in the ledger, in any period; a
// reversal whose original it does not know leaves its group incomplete.
func Summarize(period string, events []Contribution, known func(SourceKey) bool) ([]Totals, error) {
	if !ValidPeriod(period) {
		return nil, fmt.Errorf("contribution: %q is not a period", period)
	}
	type acc struct {
		t         Totals
		hashes    []string
		checksums map[string]bool
		reasons   map[IncompleteReason]bool
	}
	groups := map[Group]*acc{}
	for _, c := range events {
		if c.Period != period {
			return nil, fmt.Errorf("contribution: an event of %s summarised in %s", c.Period, period)
		}
		g := Group{LegalEntity: c.LegalEntity, Regime: c.Regime, Currency: c.Currency()}
		a, ok := groups[g]
		if !ok {
			zero, err := fiscal.ParseMoney("0", g.Currency)
			if err != nil {
				return nil, err
			}
			a = &acc{t: Totals{Group: g, Period: period, Eligible: zero, Excluded: zero, Reversed: zero},
				checksums: map[string]bool{}, reasons: map[IncompleteReason]bool{}}
			groups[g] = a
		}
		if c.SourceChecksum != "" {
			a.checksums[c.SourceChecksum] = true
		}
		if c.Status == StatusQuarantined {
			a.t.Quarantined++
			a.reasons[IncompleteQuarantined] = true
			continue
		}
		a.t.Accepted++
		if c.Amendment {
			a.t.Amendments++
		}
		var err error
		if a.t.Eligible, err = a.t.Eligible.Add(c.EligibleBase); err != nil {
			return nil, err
		}
		if a.t.Excluded, err = a.t.Excluded.Add(c.ExcludedBase); err != nil {
			return nil, err
		}
		if c.IsReversal() {
			a.t.Reversals++
			if a.t.Reversed, err = a.t.Reversed.Add(c.EligibleBase); err != nil {
				return nil, err
			}
			if known == nil || !known(*c.Reverses) {
				a.reasons[IncompleteUnresolvedReversal] = true
			}
		}
		a.hashes = append(a.hashes, c.Hash.String())
	}

	out := make([]Totals, 0, len(groups))
	for _, a := range groups {
		sort.Strings(a.hashes)
		items := make([]canonical.Value, len(a.hashes))
		for i, h := range a.hashes {
			items[i] = canonical.String(h)
		}
		d, err := canonical.Sum(canonical.Object(
			canonical.F("period", canonical.String(period)),
			canonical.F("legalEntityId", canonical.String(a.t.LegalEntity.String())),
			canonical.F("regime", canonical.String(string(a.t.Regime))),
			canonical.F("currency", canonical.String(string(a.t.Currency))),
			canonical.F("events", canonical.Array(items...)),
		))
		if err != nil {
			return nil, err
		}
		a.t.Digest = d
		for k := range a.checksums {
			a.t.SourceChecksums = append(a.t.SourceChecksums, k)
		}
		sort.Strings(a.t.SourceChecksums)
		for _, r := range []IncompleteReason{IncompleteQuarantined, IncompleteUnresolvedReversal} {
			if a.reasons[r] {
				a.t.Incomplete = append(a.t.Incomplete, r)
			}
		}
		out = append(out, a.t)
	}
	slices.SortFunc(out, func(x, y Totals) int {
		if c := strings.Compare(x.LegalEntity.String(), y.LegalEntity.String()); c != 0 {
			return c
		}
		if c := strings.Compare(string(x.Regime), string(y.Regime)); c != 0 {
			return c
		}
		return strings.Compare(string(x.Currency), string(y.Currency))
	})
	return out, nil
}
