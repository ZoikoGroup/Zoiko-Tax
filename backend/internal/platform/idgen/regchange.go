package idgen

import "github.com/zoikogroup/zoikotax/backend/internal/domain/id"

// RegulatoryChangeID returns a fresh RegulatoryChangeID.
func RegulatoryChangeID(g Generator) (id.RegulatoryChangeID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.RegulatoryChangeID{}, err
	}
	return id.NewRegulatoryChangeID(u), nil
}

// ImpactAssessmentID returns a fresh ImpactAssessmentID.
func ImpactAssessmentID(g Generator) (id.ImpactAssessmentID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.ImpactAssessmentID{}, err
	}
	return id.NewImpactAssessmentID(u), nil
}

// ReviewRecommendationID returns a fresh ReviewRecommendationID.
func ReviewRecommendationID(g Generator) (id.ReviewRecommendationID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.ReviewRecommendationID{}, err
	}
	return id.NewReviewRecommendationID(u), nil
}
