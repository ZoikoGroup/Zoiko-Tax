package port

import (
	"context"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/regchange"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// RegulatoryChangeRepository stores the regulatory change feed, its impact
// assessments and the review recommendations derived from them (Engineering
// Doc 03 §04). It is append-only: a change and a recommendation are their
// event histories, and an assessment is never altered.
type RegulatoryChangeRepository interface {
	// Lock serializes the tenant's writes to one key for the rest of the
	// transaction: a change, an intake key or a dedupe key.
	Lock(ctx context.Context, key string) error

	// CreateChange writes a change and its first events.
	CreateChange(ctx context.Context, c regchange.Change, events []regchange.Event) error
	// AppendChangeEvents writes a change's next events.
	AppendChangeEvents(ctx context.Context, changeID id.RegulatoryChangeID, events []regchange.Event) error
	// Change returns one change, folded.
	Change(ctx context.Context, changeID id.RegulatoryChangeID) (regchange.Change, error)
	// ChangeByIntakeKey returns the change recorded under an Idempotency-Key,
	// and false when there is none.
	ChangeByIntakeKey(ctx context.Context, key string) (regchange.Change, bool, error)
	// ChangeByDedupeKey returns the change of a source identity, version and
	// kind, and false when there is none.
	ChangeByDedupeKey(ctx context.Context, key canonical.Digest) (regchange.Change, bool, error)
	// ChangesBySource returns every change recorded from one source edition.
	ChangesBySource(ctx context.Context, key canonical.Digest) ([]regchange.Change, error)
	// Changes returns the tenant's most recent changes, newest first, at most
	// limit.
	Changes(ctx context.Context, limit int) ([]regchange.Change, error)

	// CreateAssessment writes an assessment.
	CreateAssessment(ctx context.Context, a regchange.Assessment) error
	// Assessment returns one assessment.
	Assessment(ctx context.Context, assessmentID id.ImpactAssessmentID) (regchange.Assessment, error)
	// AssessmentsOf returns a change's assessments, newest first: the first
	// is the change's current assessment.
	AssessmentsOf(ctx context.Context, changeID id.RegulatoryChangeID) ([]regchange.Assessment, error)
	// Assessments returns the tenant's most recent assessments, newest
	// first, at most limit.
	Assessments(ctx context.Context, limit int) ([]regchange.Assessment, error)

	// CreateRecommendation writes a recommendation and its DRAFTED event.
	CreateRecommendation(ctx context.Context, r regchange.Recommendation, drafted regchange.RecEvent) error
	// AppendRecommendationEvent writes a recommendation's next event.
	AppendRecommendationEvent(ctx context.Context, recommendationID id.ReviewRecommendationID, e regchange.RecEvent) error
	// Recommendation returns one recommendation, folded.
	Recommendation(ctx context.Context, recommendationID id.ReviewRecommendationID) (regchange.Recommendation, error)
	// RecommendationByIntakeKey returns the recommendation drafted under an
	// Idempotency-Key, and false when there is none.
	RecommendationByIntakeKey(ctx context.Context, key string) (regchange.Recommendation, bool, error)
	// RecommendationsOf returns a change's recommendations, newest first.
	RecommendationsOf(ctx context.Context, changeID id.RegulatoryChangeID) ([]regchange.Recommendation, error)
	// Recommendations returns the tenant's most recent recommendations,
	// newest first, at most limit.
	Recommendations(ctx context.Context, limit int) ([]regchange.Recommendation, error)
}

// ContentCoverage is the active content bundle's coverage at an instant, as
// an impact assessment reads it.
type ContentCoverage interface {
	// CoverageAt returns the components in force at t, and a
	// NO_CONTENT_BUNDLE error when the cell has no location-aware bundle.
	CoverageAt(t time.Time) (regchange.ContentSnapshot, error)
}
