package id

import "github.com/google/uuid"

// EntitlementID identifies one entitlement grant (Engineering Doc 18 §05
// TenantGrant): a contract right for one legal entity, jurisdiction,
// service, mode and operation. It is kept in its own file so the commercial
// entitlement surface adds a type without touching the shared list in id.go.
type EntitlementID struct{ base }

// NewEntitlementID wraps a raw UUID as an EntitlementID.
func NewEntitlementID(u uuid.UUID) EntitlementID { return EntitlementID{base{u}} }

// ParseEntitlementID reads an EntitlementID from its canonical string form.
func ParseEntitlementID(s string) (EntitlementID, error) {
	u, err := parse("entitlement id", s)
	if err != nil {
		return EntitlementID{}, err
	}
	return NewEntitlementID(u), nil
}

// SubscriptionPlanID identifies one subscription plan revision: a tenant's
// plan, term and included quotas as proposed and approved (Doc 18 W04, W08).
type SubscriptionPlanID struct{ base }

// NewSubscriptionPlanID wraps a raw UUID as a SubscriptionPlanID.
func NewSubscriptionPlanID(u uuid.UUID) SubscriptionPlanID { return SubscriptionPlanID{base{u}} }

// ParseSubscriptionPlanID reads a SubscriptionPlanID from its canonical
// string form.
func ParseSubscriptionPlanID(s string) (SubscriptionPlanID, error) {
	u, err := parse("subscription plan id", s)
	if err != nil {
		return SubscriptionPlanID{}, err
	}
	return NewSubscriptionPlanID(u), nil
}
