package port

import (
	"context"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/support"
)

// SupportCaseRepository stores support cases: a header written once and an
// append-only history of events (Engineering Doc 18 §5, W16). It holds no
// method that writes anything else, so a support case has no path to a
// fiscal record (SUP-03, SUP-04, SUP-11).
type SupportCaseRepository interface {
	// Lock serializes the tenant's writes to one case for the rest of the
	// transaction.
	Lock(ctx context.Context, caseID id.SupportCaseID) error
	// Create records a case and its OPENED event.
	Create(ctx context.Context, c support.Case, opened support.Event) error
	// Append records the next event of a case.
	Append(ctx context.Context, caseID id.SupportCaseID, e support.Event) error
	// Case returns one case, folded.
	Case(ctx context.Context, caseID id.SupportCaseID) (support.Case, error)
	// List returns the tenant's most recently opened cases, newest first,
	// folded.
	List(ctx context.Context, limit int) ([]support.Case, error)
}
