package port

import (
	"context"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/crosswalk"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rulechange"
)

// CrosswalkRepository stores vendor crosswalks. It is append-only: a
// crosswalk, a version and its entries are written once, and a version's
// status is the fold of its events.
type CrosswalkRepository interface {
	// Lock serializes the tenant's writes to one key for the rest of the
	// transaction.
	Lock(ctx context.Context, key string) error
	// CreateCrosswalk writes a crosswalk's identity.
	CreateCrosswalk(ctx context.Context, c crosswalk.Crosswalk) error
	// CreateVersion writes a version, its entries and its first events.
	CreateVersion(ctx context.Context, crosswalkID id.CrosswalkID, v crosswalk.Version) error
	// AppendEvent writes a version's next event.
	AppendEvent(ctx context.Context, crosswalkID id.CrosswalkID, version int, e crosswalk.Event) error
	// Crosswalk returns one crosswalk with every version, folded.
	Crosswalk(ctx context.Context, crosswalkID id.CrosswalkID) (crosswalk.Crosswalk, error)
	// CrosswalkByIdentity returns the crosswalk of a vendor namespace at a
	// vendor release, and false when there is none.
	CrosswalkByIdentity(ctx context.Context, vendor, namespace, release string) (crosswalk.Crosswalk, bool, error)
	// CrosswalkByIntakeKey returns the crosswalk one of whose versions was
	// written under an Idempotency-Key, and false when there is none.
	CrosswalkByIntakeKey(ctx context.Context, key string) (crosswalk.Crosswalk, bool, error)
	// Crosswalks returns the tenant's crosswalks, most recently created
	// first, at most limit.
	Crosswalks(ctx context.Context, limit int) ([]crosswalk.Crosswalk, error)
}

// RuleChangeRepository stores the rule approval queue. It is append-only.
type RuleChangeRepository interface {
	// Lock serializes the tenant's writes to one key for the rest of the
	// transaction.
	Lock(ctx context.Context, key string) error
	// CreateRuleChange writes a proposal and its first events.
	CreateRuleChange(ctx context.Context, p rulechange.Proposal, events []rulechange.Event) error
	// AppendRuleChangeEvent writes a proposal's next event.
	AppendRuleChangeEvent(ctx context.Context, changeID id.RuleChangeID, e rulechange.Event) error
	// RuleChange returns one proposal, folded.
	RuleChange(ctx context.Context, changeID id.RuleChangeID) (rulechange.Proposal, error)
	// RuleChangeByIntakeKey returns the proposal recorded under an
	// Idempotency-Key, and false when there is none.
	RuleChangeByIntakeKey(ctx context.Context, key string) (rulechange.Proposal, bool, error)
	// RuleChanges returns the tenant's proposals, newest first, at most
	// limit.
	RuleChanges(ctx context.Context, limit int) ([]rulechange.Proposal, error)
}
