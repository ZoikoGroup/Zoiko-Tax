package fiscal

import (
	"fmt"

	"github.com/cockroachdb/apd/v3"
)

// RoundingMode is the closed set from ADR-0002 §2.4. It is serialized into
// evidence by name, never by ordinal, so that a decision recorded today is
// still legible after a member is added.
type RoundingMode string

const (
	RoundHalfUp   RoundingMode = "HALF_UP"
	RoundHalfEven RoundingMode = "HALF_EVEN"
	RoundHalfDown RoundingMode = "HALF_DOWN"
	RoundDown     RoundingMode = "DOWN"
	RoundUp       RoundingMode = "UP"
	RoundCeiling  RoundingMode = "CEILING"
	RoundFloor    RoundingMode = "FLOOR"
)

func (m RoundingMode) rounder() apd.Rounder {
	switch m {
	case RoundHalfUp:
		return apd.RoundHalfUp
	case RoundHalfEven:
		return apd.RoundHalfEven
	case RoundHalfDown:
		return apd.RoundHalfDown
	case RoundDown:
		return apd.RoundDown
	case RoundUp:
		return apd.RoundUp
	case RoundCeiling:
		return apd.RoundCeiling
	case RoundFloor:
		return apd.RoundFloor
	}
	// Unreachable: validate() rejects unknown modes before any arithmetic runs.
	return ""
}

func (m RoundingMode) valid() bool {
	switch m {
	case RoundHalfUp, RoundHalfEven, RoundHalfDown, RoundDown, RoundUp, RoundCeiling, RoundFloor:
		return true
	}
	return false
}

// RoundingBasis names the point in a calculation at which a policy applies.
// Which basis a rule uses is content, not a runtime choice. ADR-0002 §2.3.
type RoundingBasis string

const (
	BasisLine              RoundingBasis = "LINE"
	BasisDocument          RoundingBasis = "DOCUMENT"
	BasisTaxComponent      RoundingBasis = "TAX_COMPONENT"
	BasisJurisdictionTotal RoundingBasis = "JURISDICTION_TOTAL"
)

func (b RoundingBasis) valid() bool {
	switch b {
	case BasisLine, BasisDocument, BasisTaxComponent, BasisJurisdictionTotal:
		return true
	}
	return false
}

// MaxScale bounds the scale a policy may request. It is well above any
// currency minor unit and above the precision of any rate we expect to
// encounter, and it exists so that malformed content fails loudly.
const MaxScale = 12

// RoundingPolicy is the rounding instruction for one operation.
//
// Every value of this type that reaches the runtime originates in a RuleVersion
// inside a signed content bundle. Construction from a literal belongs in
// fiscaltest, which production code cannot import. ADR-0002 §2.3.
type RoundingPolicy struct {
	Mode  RoundingMode
	Scale int32
	Basis RoundingBasis
}

func (p RoundingPolicy) validate() error {
	if !p.Mode.valid() {
		return fmt.Errorf("fiscal: unknown rounding mode %q", p.Mode)
	}
	if !p.Basis.valid() {
		return fmt.Errorf("fiscal: unknown rounding basis %q", p.Basis)
	}
	if p.Scale < 0 || p.Scale > MaxScale {
		return fmt.Errorf("fiscal: scale %d out of range [0,%d]", p.Scale, MaxScale)
	}
	return nil
}

// String renders the policy in the form recorded in evidence.
func (p RoundingPolicy) String() string {
	return fmt.Sprintf("%s@%d/%s", p.Mode, p.Scale, p.Basis)
}
