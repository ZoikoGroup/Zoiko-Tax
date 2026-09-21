package fiscal

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/cockroachdb/apd/v3"
)

// RoundingMode is the closed set from ADR-0002 §2.4. It is serialized into
// evidence by name, never by ordinal, so that a decision recorded today is
// still legible after a member is added.
type RoundingMode string

// The rounding modes, mapped 1:1 onto apd's rounders. Adding a member is a
// SCHEMA train change with a new golden-vector set (ADR-0002 §2.4).
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

// The points at which a policy may apply. Which one a rule uses is stated by
// the authority instrument the rule implements, never inferred.
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
// The fields are unexported and there is no literal constructor, because
// ADR-0002 §2.3 puts the origin of every policy in a RuleVersion inside a
// signed content bundle. DecodeRoundingPolicy is that path; a composite
// literal elsewhere yields the zero value, which fails validation on first
// use with "unknown rounding mode".
//
// Tests construct policies through internal/fiscaltest, which production code
// may not import (.golangci.yml, rule policy-literals-are-test-only).
type RoundingPolicy struct {
	mode  RoundingMode
	scale int32
	basis RoundingBasis
}

// roundingPolicyJSON is the wire form carried by content and by evidence.
//
// The fields are pointers so that an absent key is distinguishable from a
// present zero. Scale 0 is legal — JPY rounds at scale 0, and so does a
// jurisdiction that rounds tax to the whole unit — so a missing scale must
// never be read as a scale of zero.
type roundingPolicyJSON struct {
	Mode  *RoundingMode  `json:"mode"`
	Scale *int32         `json:"scale"`
	Basis *RoundingBasis `json:"basis"`
}

// DecodeRoundingPolicy reads a policy from its content-bundle form:
//
//	{"mode":"HALF_UP","scale":2,"basis":"LINE"}
//
// Unknown keys are rejected: content is signed and versioned, so a key this
// build does not understand means the bundle was authored against a different
// SCHEMA train version, which is a fault rather than something to ignore.
func DecodeRoundingPolicy(data []byte) (RoundingPolicy, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var raw roundingPolicyJSON
	if err := dec.Decode(&raw); err != nil {
		return RoundingPolicy{}, fmt.Errorf("fiscal: decode rounding policy: %w", err)
	}
	switch {
	case raw.Mode == nil:
		return RoundingPolicy{}, fmt.Errorf("fiscal: decode rounding policy: no mode")
	case raw.Scale == nil:
		return RoundingPolicy{}, fmt.Errorf("fiscal: decode rounding policy: no scale")
	case raw.Basis == nil:
		return RoundingPolicy{}, fmt.Errorf("fiscal: decode rounding policy: no basis")
	}

	p := RoundingPolicy{mode: *raw.Mode, scale: *raw.Scale, basis: *raw.Basis}
	if err := p.validate(); err != nil {
		return RoundingPolicy{}, err
	}
	return p, nil
}

// UnmarshalJSON lets a policy be decoded in place inside a larger content
// document. It carries the same rejections as DecodeRoundingPolicy.
func (p *RoundingPolicy) UnmarshalJSON(data []byte) error {
	decoded, err := DecodeRoundingPolicy(data)
	if err != nil {
		return err
	}
	*p = decoded
	return nil
}

// MarshalJSON renders the form recorded in a TaxDecision's evidence
// (ADR-0002 §5.1 control 3). Mode and basis are written by name; no ordinal
// appears anywhere in the encoding, so an evidence record stays readable after
// a mode is added to the enum.
func (p RoundingPolicy) MarshalJSON() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	// Field order is fixed by declaration order and is part of the evidence
	// form. It will be superseded by the canon profile when ADR-0011 lands.
	return json.Marshal(roundingPolicyJSON{Mode: &p.mode, Scale: &p.scale, Basis: &p.basis})
}

// Mode reports the rounding mode the content named.
func (p RoundingPolicy) Mode() RoundingMode { return p.mode }

// Scale reports the digits after the decimal point at which rounding occurs.
func (p RoundingPolicy) Scale() int32 { return p.scale }

// Basis reports the point in a calculation at which the policy applies.
func (p RoundingPolicy) Basis() RoundingBasis { return p.basis }

func (p RoundingPolicy) validate() error {
	if !p.mode.valid() {
		return fmt.Errorf("fiscal: unknown rounding mode %q", p.mode)
	}
	if !p.basis.valid() {
		return fmt.Errorf("fiscal: unknown rounding basis %q", p.basis)
	}
	if p.scale < 0 || p.scale > MaxScale {
		return fmt.Errorf("fiscal: scale %d out of range [0,%d]", p.scale, MaxScale)
	}
	return nil
}

// String renders the policy in the short form used in error messages and logs.
func (p RoundingPolicy) String() string {
	return fmt.Sprintf("%s@%d/%s", p.mode, p.scale, p.basis)
}
