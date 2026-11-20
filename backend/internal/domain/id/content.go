package id

import "github.com/google/uuid"

// CrosswalkID identifies one vendor crosswalk: the mapping of one vendor's
// code namespace, at one vendor release, onto Zoiko canonical codes (Doc 02
// VXC). Its successive mapping versions share it. It is kept in its own file
// so the content workbench adds types without touching the shared list in
// id.go.
type CrosswalkID struct{ base }

// NewCrosswalkID wraps a raw UUID as a CrosswalkID.
func NewCrosswalkID(u uuid.UUID) CrosswalkID { return CrosswalkID{base{u}} }

// ParseCrosswalkID reads a CrosswalkID from its canonical string form.
func ParseCrosswalkID(s string) (CrosswalkID, error) {
	u, err := parse("crosswalk id", s)
	if err != nil {
		return CrosswalkID{}, err
	}
	return NewCrosswalkID(u), nil
}

// RuleChangeID identifies one proposed rule change awaiting specialist review
// and independent approval (CONT-001 §3 ContentChange).
type RuleChangeID struct{ base }

// NewRuleChangeID wraps a raw UUID as a RuleChangeID.
func NewRuleChangeID(u uuid.UUID) RuleChangeID { return RuleChangeID{base{u}} }

// ParseRuleChangeID reads a RuleChangeID from its canonical string form.
func ParseRuleChangeID(s string) (RuleChangeID, error) {
	u, err := parse("rule change id", s)
	if err != nil {
		return RuleChangeID{}, err
	}
	return NewRuleChangeID(u), nil
}
