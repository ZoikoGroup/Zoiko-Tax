package port

import (
	"context"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/entitlement"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subscription"
)

// EntitlementRepository stores entitlement grants and subscription plan
// revisions (Engineering Doc 18 §05 TenantGrant, CommercialSKU). It is
// append-only: a record row is never updated or deleted, and its state is
// the fold of its events. It holds no method that writes anything else, so
// a grant or a plan has no path to content, a decision or a rule.
type EntitlementRepository interface {
	// Lock serializes the tenant's writes to one key for the rest of the
	// transaction: a record, an intake key or a grant scope.
	Lock(ctx context.Context, key string) error

	// CreateGrant writes a grant and its first events.
	CreateGrant(ctx context.Context, g entitlement.Grant, events []entitlement.Event) error
	// AppendGrantEvents writes a grant's next events.
	AppendGrantEvents(ctx context.Context, grantID id.EntitlementID, events []entitlement.Event) error
	// Grant returns one grant, folded.
	Grant(ctx context.Context, grantID id.EntitlementID) (entitlement.Grant, error)
	// GrantByIntakeKey returns the grant created under an Idempotency-Key,
	// and false when there is none.
	GrantByIntakeKey(ctx context.Context, key string) (entitlement.Grant, bool, error)
	// Grants returns the tenant's most recent grants, newest first, at most
	// limit.
	Grants(ctx context.Context, limit int) ([]entitlement.Grant, error)

	// CreatePlan writes a plan revision, its quotas and its PROPOSED event.
	CreatePlan(ctx context.Context, p subscription.Plan, events []subscription.Event) error
	// AppendPlanEvent writes a plan revision's next event.
	AppendPlanEvent(ctx context.Context, planID id.SubscriptionPlanID, e subscription.Event) error
	// Plan returns one plan revision, folded.
	Plan(ctx context.Context, planID id.SubscriptionPlanID) (subscription.Plan, error)
	// PlanByIntakeKey returns the plan revision proposed under an
	// Idempotency-Key, and false when there is none.
	PlanByIntakeKey(ctx context.Context, key string) (subscription.Plan, bool, error)
	// Plans returns the tenant's most recent plan revisions, newest first,
	// at most limit.
	Plans(ctx context.Context, limit int) ([]subscription.Plan, error)
}

// UsageBasisRow is one row a meter counted: its identifier and the instant
// that placed it in the period.
type UsageBasisRow struct {
	ID string
	At time.Time
}

// UsageRepository counts the cell's own records for the meters
// (subscription.Definitions). It reads; it writes nothing.
type UsageRepository interface {
	// Counts returns every meter's count and basis digest over [from, to).
	Counts(ctx context.Context, from, to time.Time) ([]subscription.Count, error)
	// Basis returns one meter's count and digest over [from, to) and the
	// first rows it counted, in the digest's order, at most limit — all read
	// in one statement, so the rows and the count are one snapshot.
	Basis(ctx context.Context, m subscription.Meter, from, to time.Time, limit int) (subscription.Count, []UsageBasisRow, error)
}
