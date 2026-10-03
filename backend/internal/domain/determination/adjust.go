package determination

import (
	"fmt"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
)

// AdjustmentKind is DET-001 §10.4's closed set.
type AdjustmentKind string

// The adjustment kinds.
const (
	// AdjustFullReversal negates every component of the original.
	AdjustFullReversal AdjustmentKind = "FULL_REVERSAL"
	// AdjustPartialCredit credits a stated tax amount, allocated (§10.5).
	AdjustPartialCredit AdjustmentKind = "PARTIAL_CREDIT"
	// AdjustCorrection supersedes the original with a re-determination on
	// corrected input. It is a new decision, made by Evaluate against the
	// original's bundle; this package has nothing to add to it.
	AdjustCorrection AdjustmentKind = "CORRECTION"
	// AdjustRefund is a payment-side reversal that references the original
	// and does not re-determine it.
	AdjustRefund AdjustmentKind = "REFUND"
)

// Valid reports whether k is a known kind.
func (k AdjustmentKind) Valid() bool {
	switch k {
	case AdjustFullReversal, AdjustPartialCredit, AdjustCorrection, AdjustRefund:
		return true
	}
	return false
}

// Reverse is a FULL_REVERSAL: every net and every component amount negated,
// nothing recomputed. A reversal of a determination nets to zero by
// construction (DET-001 §14.2, adjustment closure), because negation is exact.
func Reverse(d Determination) Determination {
	out := Determination{Currency: d.Currency, Lines: make([]LineResult, len(d.Lines))}
	for i, l := range d.Lines {
		lr := LineResult{Key: l.Key, Net: l.Net.Neg(), Components: make([]ComponentResult, len(l.Components))}
		for j, c := range l.Components {
			r := c
			r.Raw = c.Raw.Neg()
			r.Amount = c.Amount.Neg()
			if c.Base != nil {
				b := c.Base.Neg()
				r.Base = &b
			}
			lr.Components[j] = r
			out.Trace = append(out.Trace, TraceStep{
				Line: l.Key, Component: c.ID(), Step: string(AdjustFullReversal),
				Detail: fmt.Sprintf("%s -> %s", c.Amount.CanonicalString(), r.Amount.CanonicalString()),
			})
		}
		out.Lines[i] = lr
	}
	return out
}

// ID returns the component the result is for.
func (c ComponentResult) ID() ComponentID { return c.Component }

// PartialCredit credits tax of the stated amount against an original
// determination (DET-001 §10.5).
//
// The credit is allocated across the original's lines and components by
// largest remainder, weighted by each component's share of the original's
// tax, in the original's line and evaluation order. No rate is applied to the
// credit: re-applying rates recomputes rather than reverses, and the two
// differ wherever the original rounded (DET-REQ-0031). The result carries
// negative amounts and zero nets — a partial credit moves tax, not supply.
//
// credit is a positive amount no greater than the original's total tax. The
// policy is the scale the credit is materialised at.
func PartialCredit(original Determination, credit fiscal.Money, p fiscal.RoundingPolicy) (Determination, error) {
	if credit.Currency() != original.Currency {
		return Determination{}, errs.Invalid("credit", errs.ReasonCurrencyMismatch,
			fmt.Sprintf("A credit in %s cannot adjust a determination in %s.", credit.Currency(), original.Currency))
	}
	if credit.Sign() <= 0 {
		return Determination{}, errs.Invalid("credit", errs.ReasonInvalidValue, "A partial credit is a positive amount of tax.")
	}

	type slot struct{ line, comp int }
	var slots []slot
	var weights []fiscal.Money
	total, err := fiscal.ParseMoney("0", original.Currency)
	if err != nil {
		return Determination{}, err
	}
	for i, l := range original.Lines {
		for j, c := range l.Components {
			if c.Amount.Sign() < 0 {
				return Determination{}, errs.Invalid("original", errs.ReasonInvalidValue,
					"A partial credit adjusts a charge; the original carries a negative component.")
			}
			slots = append(slots, slot{i, j})
			weights = append(weights, c.Amount)
			if total, err = total.Add(c.Amount); err != nil {
				return Determination{}, err
			}
		}
	}
	if len(slots) == 0 {
		return Determination{}, errs.Invalid("original", errs.ReasonInvalidValue, "The original determination carries no tax to credit.")
	}
	if over, err := credit.Cmp(total); err != nil {
		return Determination{}, err
	} else if over > 0 {
		return Determination{}, errs.Invalid("credit", errs.ReasonInvalidValue,
			fmt.Sprintf("A credit of %s exceeds the %s of tax the original charged.", credit.CanonicalString(), total.CanonicalString()))
	}

	parts, err := fiscal.Allocate(credit.Neg(), weights, p)
	if err != nil {
		return Determination{}, err
	}
	out := Determination{Currency: original.Currency, Lines: make([]LineResult, len(original.Lines))}
	for i, l := range original.Lines {
		out.Lines[i] = LineResult{Key: l.Key, Net: l.Net.Zero()}
	}
	for k, s := range slots {
		orig := original.Lines[s.line].Components[s.comp]
		out.Lines[s.line].Components = append(out.Lines[s.line].Components, ComponentResult{
			Component: orig.Component, Jurisdiction: orig.Jurisdiction, TaxType: orig.TaxType,
			Inclusive: orig.Inclusive, Raw: parts[k], Amount: parts[k],
		})
		out.Trace = append(out.Trace, TraceStep{
			Line: original.Lines[s.line].Key, Component: orig.Component, Step: string(AdjustPartialCredit),
			Detail: fmt.Sprintf("%s of %s, weighted by %s", parts[k].CanonicalString(), credit.CanonicalString(), orig.Amount.CanonicalString()),
		})
	}
	return out, nil
}

// PositionKey names one (line, component) cell of a net position.
type PositionKey struct {
	Line      string
	Component ComponentID
}

// NetPosition sums an adjustment chain cell by cell (DET-001 §10.7). The
// chain's net effect is derivable only by summing it, so this is the one
// function that does, and the property test asserts that a determination
// followed by its reversal sums to zero in every cell.
func NetPosition(chain ...Determination) (map[PositionKey]fiscal.Money, error) {
	out := map[PositionKey]fiscal.Money{}
	var currency fiscal.Currency
	for i, d := range chain {
		if i == 0 {
			currency = d.Currency
		} else if d.Currency != currency {
			return nil, errs.Invalid("chain", errs.ReasonCurrencyMismatch,
				fmt.Sprintf("Adjustment %d is in %s; the chain is in %s.", i, d.Currency, currency))
		}
		for _, l := range d.Lines {
			for _, c := range l.Components {
				k := PositionKey{Line: l.Key, Component: c.Component}
				prior, ok := out[k]
				if !ok {
					out[k] = c.Amount
					continue
				}
				sum, err := prior.Add(c.Amount)
				if err != nil {
					return nil, err
				}
				out[k] = sum
			}
		}
	}
	return out, nil
}
