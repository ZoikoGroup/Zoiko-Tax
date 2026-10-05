package obligation

import (
	"fmt"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/accumulator"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
)

// ThresholdKind is ZTAX-DET-001 §9.3's closed set.
type ThresholdKind string

// The threshold kinds.
const (
	ThresholdRegistration ThresholdKind = "REGISTRATION"
	ThresholdDeMinimis    ThresholdKind = "DE_MINIMIS"
	ThresholdCap          ThresholdKind = "CAP"
	ThresholdBracket      ThresholdKind = "BRACKET"
)

// CrossingMode is what happens to the transaction that crosses
// (ZTAX-DET-001 §9.4).
type CrossingMode string

// The crossing modes.
const (
	// CrossProspective: the new treatment applies from the next transaction.
	CrossProspective CrossingMode = "PROSPECTIVE"
	// CrossInclusive: the new treatment applies to the crossing transaction
	// in full.
	CrossInclusive CrossingMode = "INCLUSIVE"
	// CrossSplit: the crossing transaction is split at the limit, each part
	// under its own side.
	CrossSplit CrossingMode = "SPLIT"
)

// ThresholdState is where a running total stands. UNKNOWN is a state of its
// own and is never read as BELOW (ZTAX-OBL-REQ-0071): a registration duty that
// is not known to be untriggered is not untriggered.
type ThresholdState string

// The states.
const (
	ThresholdBelow   ThresholdState = "BELOW"
	ThresholdReached ThresholdState = "REACHED"
	ThresholdUnknown ThresholdState = "UNKNOWN"
)

// Threshold is a threshold rule from content.
type Threshold struct {
	ID    string
	Kind  ThresholdKind
	Limit fiscal.Money
	// Comparison is explicit — "exceeds" (GT) and "reaches" (GTE) differ by
	// one minor unit at exactly the limit (ZTAX-OBL-REQ-0066,
	// ZTAX-DET-REQ-0029).
	Comparison accumulator.Comparison
	// Crossing is declared. A CAP that does not declare one is SPLIT, the one
	// default DET-001 §9.5 states; every other kind must declare
	// (ZTAX-DET-REQ-0026).
	Crossing CrossingMode
	// DeMinimisState is the pack's name for the legal state below a
	// de-minimis limit (ZTAX-OBL-REQ-0070). De minimis is a legal status a
	// pack defines, not "the number was small".
	DeMinimisState string
	// Window is the period rule that bounds a rolling threshold.
	Window *PeriodRule
}

// Mode returns the effective crossing mode.
func (t Threshold) Mode() CrossingMode {
	if t.Crossing == "" && t.Kind == ThresholdCap {
		return CrossSplit
	}
	return t.Crossing
}

// Validate refuses a threshold that does not say what it means.
func (t Threshold) Validate() error {
	switch t.Kind {
	case ThresholdRegistration, ThresholdDeMinimis, ThresholdCap, ThresholdBracket:
	default:
		return fmt.Errorf("threshold %s has kind %q", t.ID, t.Kind)
	}
	if t.ID == "" {
		return fmt.Errorf("threshold has no id")
	}
	if t.Limit.Currency() == "" {
		return fmt.Errorf("threshold %s has no limit", t.ID)
	}
	if !t.Comparison.Valid() {
		return fmt.Errorf("threshold %s does not declare whether the limit itself crosses", t.ID)
	}
	switch t.Mode() {
	case CrossProspective, CrossInclusive, CrossSplit:
	default:
		return fmt.Errorf("threshold %s declares no crossing mode", t.ID)
	}
	if t.Kind == ThresholdDeMinimis && t.DeMinimisState == "" {
		return fmt.Errorf("de-minimis threshold %s names no legal state", t.ID)
	}
	if t.Window != nil {
		if t.Window.Kind != PeriodRolling {
			return fmt.Errorf("threshold %s has a %s window; a threshold window is ROLLING", t.ID, t.Window.Kind)
		}
		if err := t.Window.Validate(); err != nil {
			return fmt.Errorf("threshold %s: %w", t.ID, err)
		}
	}
	return nil
}

// ThresholdResult is one evaluation, with its before and after state recorded
// for audit (ZTAX-OBL-REQ-0068).
type ThresholdResult struct {
	State   ThresholdState
	Crossed bool
	// Before and After are nil when the prior total is unknown.
	Before *fiscal.Money
	After  *fiscal.Money
	// UnderPrior and UnderNew are the crossing contribution's two parts under
	// the crossing mode: the amount treated under the old side and under the
	// new. On a transaction that does not cross, UnderPrior is the whole
	// contribution. They sum to the contribution exactly.
	UnderPrior fiscal.Money
	UnderNew   fiscal.Money
	// Latched says the state is REACHED because a prior crossing holds, not
	// because the current total does.
	Latched bool
}

// Evaluate applies a contribution to a prior total.
//
// before is nil when the prior total is not known; the result is then
// UNKNOWN, whatever the contribution. latched says the threshold was crossed
// earlier: a total that has since fallen back below the limit does not
// un-cross it, and the duty that crossing created is not cancelled by the fall
// (ZTAX-OBL-REQ-0069).
func (t Threshold) Evaluate(before *fiscal.Money, contribution fiscal.Money, latched bool) (ThresholdResult, error) {
	if err := t.Validate(); err != nil {
		return ThresholdResult{}, err
	}
	if contribution.Currency() != t.Limit.Currency() {
		return ThresholdResult{}, fmt.Errorf("threshold %s is in %s; the contribution is in %s", t.ID, t.Limit.Currency(), contribution.Currency())
	}
	if before == nil {
		return ThresholdResult{State: ThresholdUnknown, UnderPrior: contribution, UnderNew: contribution.Zero()}, nil
	}
	after, err := before.Add(contribution)
	if err != nil {
		return ThresholdResult{}, err
	}
	reachedBefore, err := t.reached(*before)
	if err != nil {
		return ThresholdResult{}, err
	}
	reachedAfter, err := t.reached(after)
	if err != nil {
		return ThresholdResult{}, err
	}
	b := *before
	res := ThresholdResult{Before: &b, After: &after, UnderPrior: contribution, UnderNew: contribution.Zero()}
	switch {
	case reachedAfter:
		res.State = ThresholdReached
	case latched:
		res.State, res.Latched = ThresholdReached, true
	default:
		res.State = ThresholdBelow
	}
	if latched || reachedBefore || !reachedAfter {
		if reachedBefore || latched {
			// Wholly on the new side already.
			res.UnderPrior, res.UnderNew = contribution.Zero(), contribution
		}
		return res, nil
	}

	res.Crossed = true
	switch t.Mode() {
	case CrossProspective:
		// The crossing transaction stays on the old side in full.
	case CrossInclusive:
		res.UnderPrior, res.UnderNew = contribution.Zero(), contribution
	case CrossSplit:
		below, err := t.Limit.Sub(*before)
		if err != nil {
			return ThresholdResult{}, err
		}
		above, err := contribution.Sub(below)
		if err != nil {
			return ThresholdResult{}, err
		}
		res.UnderPrior, res.UnderNew = below, above
	}
	return res, nil
}

func (t Threshold) reached(total fiscal.Money) (bool, error) {
	c, err := total.Cmp(t.Limit)
	if err != nil {
		return false, err
	}
	if t.Comparison == accumulator.AtOrAbove {
		return c >= 0, nil
	}
	return c > 0, nil
}
