package port

import (
	"context"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/classification"
)

// ClassificationDecisionRepository stores reviewer decisions on AI
// classification proposals. It is append-only: it has no method that updates or
// deletes a decision, and a second decision for the same proposal is a
// conflict. Every method is scoped to the caller's tenant.
type ClassificationDecisionRepository interface {
	// Lock serializes the tenant's writes for one proposal for the rest of
	// the transaction.
	Lock(ctx context.Context, proposalID uuid.UUID) error
	// Create records a decision. A second decision for the same proposal is
	// CategoryConflict.
	Create(ctx context.Context, d classification.ReviewDecision) error
	// Decision returns the decision for a proposal, or CategoryNotFound.
	Decision(ctx context.Context, proposalID uuid.UUID) (classification.ReviewDecision, error)
}
