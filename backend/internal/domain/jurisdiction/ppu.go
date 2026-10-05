package jurisdiction

import (
	"fmt"
	"sort"
	"time"
)

// PrimaryPlaceOfUse is a held, contractual attribute of a customer's service
// (JUR-001 §5.1): the address the customer declared, already mapped to a
// jurisdiction, for an effective period. A customer who moves has two, with
// adjacent periods.
type PrimaryPlaceOfUse struct {
	Jurisdiction ID
	From         time.Time
	// To is exclusive; nil is open-ended.
	To *time.Time
	// Source and CollectedAt describe the declaration, and are carried onto
	// the evidence item a transaction resolves with.
	Source      EvidenceSource
	CollectedAt time.Time
}

// ValidatePPU checks a declaration against the graph when it is recorded, not
// when it is used (JUR-001 §5.2): the jurisdiction must exist for the whole of
// the declared period's start. A determination is not the place to discover
// that a declared address does not exist.
func ValidatePPU(g *Graph, p PrimaryPlaceOfUse) error {
	if p.From.IsZero() {
		return fmt.Errorf("jurisdiction: PPU has no effective-from date")
	}
	if p.To != nil && !p.To.After(p.From) {
		return fmt.Errorf("jurisdiction: PPU ends on or before it starts")
	}
	if !p.Source.Valid() || p.CollectedAt.IsZero() {
		return fmt.Errorf("jurisdiction: PPU declaration has no source or no collection time")
	}
	n, ok := g.At(p.Jurisdiction, p.From)
	if !ok {
		return fmt.Errorf("jurisdiction: PPU names %s, which does not exist at %s in graph %s",
			p.Jurisdiction, p.From.Format(time.RFC3339), g.Version())
	}
	if n.Level == LevelSpecial {
		return fmt.Errorf("jurisdiction: PPU names SPECIAL %s; a place of use is a general jurisdiction", n.ID)
	}
	return nil
}

// PPUEvidenceAt returns the evidence item for the declaration effective at
// eventTime — never at decision time (JUR-REQ-0015). Periods must not
// overlap; two declarations effective at one instant are refused rather than
// one chosen.
//
// A correction to a historical period changes what this returns for that
// period, and changes nothing already decided: decisions are immutable, and a
// corrected answer is a new decision superseding the old one (JUR-001 §5.4).
func PPUEvidenceAt(ppus []PrimaryPlaceOfUse, eventTime time.Time) (SitusEvidence, bool, error) {
	ps := append([]PrimaryPlaceOfUse(nil), ppus...)
	sort.Slice(ps, func(i, j int) bool { return ps[i].From.Before(ps[j].From) })
	for i := 1; i < len(ps); i++ {
		prev := ps[i-1]
		if prev.To == nil || prev.To.After(ps[i].From) {
			return SitusEvidence{}, false, fmt.Errorf("jurisdiction: PPU periods from %s and %s overlap",
				prev.From.Format(time.RFC3339), ps[i].From.Format(time.RFC3339))
		}
	}
	for _, p := range ps {
		if !eventTime.Before(p.From) && (p.To == nil || eventTime.Before(*p.To)) {
			return SitusEvidence{
				Type: EvidencePrimaryPlaceOfUse, Source: p.Source, CollectedAt: p.CollectedAt, Jurisdiction: p.Jurisdiction,
			}, true, nil
		}
	}
	return SitusEvidence{}, false, nil
}
