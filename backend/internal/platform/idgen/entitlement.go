package idgen

import "github.com/zoikogroup/zoikotax/backend/internal/domain/id"

// EntitlementID returns a fresh EntitlementID.
func EntitlementID(g Generator) (id.EntitlementID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.EntitlementID{}, err
	}
	return id.NewEntitlementID(u), nil
}

// SubscriptionPlanID returns a fresh SubscriptionPlanID.
func SubscriptionPlanID(g Generator) (id.SubscriptionPlanID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.SubscriptionPlanID{}, err
	}
	return id.NewSubscriptionPlanID(u), nil
}
