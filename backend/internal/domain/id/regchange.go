package id

import "github.com/google/uuid"

// The regulatory change intelligence identifiers (Engineering Doc 03 §04,
// WF-05, WF-09; Sidebar WF-02). They are kept in their own file so the
// change feed adds its types without touching the shared list in id.go.

// RegulatoryChangeID identifies one recorded regulatory change: a source
// edition's one legal effect, as reported (Doc 03 §04 change_event).
type RegulatoryChangeID struct{ base }

// NewRegulatoryChangeID wraps a raw UUID as a RegulatoryChangeID.
func NewRegulatoryChangeID(u uuid.UUID) RegulatoryChangeID { return RegulatoryChangeID{base{u}} }

// ParseRegulatoryChangeID reads a RegulatoryChangeID from its canonical form.
func ParseRegulatoryChangeID(s string) (RegulatoryChangeID, error) {
	u, err := parse("regulatory change id", s)
	if err != nil {
		return RegulatoryChangeID{}, err
	}
	return NewRegulatoryChangeID(u), nil
}

// ImpactAssessmentID identifies one deterministic impact assessment of a
// change (Doc 03 §04 impact_assessment).
type ImpactAssessmentID struct{ base }

// NewImpactAssessmentID wraps a raw UUID as an ImpactAssessmentID.
func NewImpactAssessmentID(u uuid.UUID) ImpactAssessmentID { return ImpactAssessmentID{base{u}} }

// ParseImpactAssessmentID reads an ImpactAssessmentID from its canonical form.
func ParseImpactAssessmentID(s string) (ImpactAssessmentID, error) {
	u, err := parse("impact assessment id", s)
	if err != nil {
		return ImpactAssessmentID{}, err
	}
	return NewImpactAssessmentID(u), nil
}

// ReviewRecommendationID identifies one review recommendation derived from
// an impact assessment. It is the reference a later rule-approval workflow
// names; it is never itself an approval.
type ReviewRecommendationID struct{ base }

// NewReviewRecommendationID wraps a raw UUID as a ReviewRecommendationID.
func NewReviewRecommendationID(u uuid.UUID) ReviewRecommendationID {
	return ReviewRecommendationID{base{u}}
}

// ParseReviewRecommendationID reads a ReviewRecommendationID from its
// canonical form.
func ParseReviewRecommendationID(s string) (ReviewRecommendationID, error) {
	u, err := parse("review recommendation id", s)
	if err != nil {
		return ReviewRecommendationID{}, err
	}
	return NewReviewRecommendationID(u), nil
}
