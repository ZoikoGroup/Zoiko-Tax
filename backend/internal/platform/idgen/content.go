package idgen

import "github.com/zoikogroup/zoikotax/backend/internal/domain/id"

// CrosswalkID returns a fresh CrosswalkID.
func CrosswalkID(g Generator) (id.CrosswalkID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.CrosswalkID{}, err
	}
	return id.NewCrosswalkID(u), nil
}

// RuleChangeID returns a fresh RuleChangeID.
func RuleChangeID(g Generator) (id.RuleChangeID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.RuleChangeID{}, err
	}
	return id.NewRuleChangeID(u), nil
}
