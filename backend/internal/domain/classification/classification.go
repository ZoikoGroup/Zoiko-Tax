// Package classification assigns an item to a tax ontology node.
//
// The design constraint that shapes this whole package is ADR-0006 §2.6 and
// ADR-0019 C2, which are the same rule at two layers: there is no route from an
// AI suggestion to an authoritative fiscal record except through human review.
// Build Plan §8 rates that route as a High risk.
//
// So the rule is not expressed as a boolean field that somebody checks. A
// Classification carries its Source, Authoritative() refuses anything an AI
// produced that a human has not confirmed, and the confirmation cannot be
// recorded without naming who made it. A call site that wants to use a
// classification in a fiscal path asks Authoritative(), and there is no
// argument it can pass to make an unconfirmed suggestion say yes.
package classification

import (
	"fmt"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// OntologyID names a node in the tax ontology. Like a jurisdiction ID it is a
// stable, human-meaningful content identifier, not a UUID (ADR-0012 §2.4):
//
//	ontology:telecom/voice/mobile/roaming-outbound
type OntologyID string

// Validate applies the grammar.
func (o OntologyID) Validate() error {
	s := string(o)
	if !strings.HasPrefix(s, "ontology:") {
		return fmt.Errorf("classification: %q does not carry the ontology: prefix", o)
	}
	if strings.TrimPrefix(s, "ontology:") == "" {
		return fmt.Errorf("classification: %q names nothing", o)
	}
	return nil
}

// Source is what produced a classification. It is the field ADR-0006 §2.6 turns
// on, and it is closed so that a new source has to be considered rather than
// appearing as a string somebody wrote.
type Source string

// The sources.
const (
	// SourceRule is a deterministic classification from content. Authoritative
	// on its own.
	SourceRule Source = "RULE"
	// SourceAISuggested came from the Intelligence Fabric through the Governed
	// Model Gateway. Never authoritative without a human confirmation.
	SourceAISuggested Source = "AI_SUGGESTED"
	// SourceHuman was chosen by a person. Authoritative on its own.
	SourceHuman Source = "HUMAN"
)

// Outcome is what the classification concluded.
type Outcome string

// The outcomes. Three of the four are refusals, and they are recorded with
// evidence rather than returned as errors (ADR-0016 §2.1).
const (
	OutcomeClassified     Outcome = "CLASSIFIED"
	OutcomeAmbiguous      Outcome = "AMBIGUOUS"
	OutcomeUnsupported    Outcome = "UNSUPPORTED"
	OutcomeReviewRequired Outcome = "REVIEW_REQUIRED"
)

// Confirmation records a human accepting a suggestion.
//
// Both fields are required together, and the database enforces the same pairing
// (migration 000003, classification_confirmation_complete). Two expressions of
// one invariant, neither load-bearing alone.
type Confirmation struct {
	By id.UserID
	At time.Time
}

// Classification is one item's assignment.
type Classification struct {
	ID       id.ClassificationID
	TenantID id.TenantID

	// BusinessKey is stable across re-classifications; a correction is a new
	// row linked by Supersedes (ADR-0003 §2.2).
	BusinessKey string
	Supersedes  *id.ClassificationID
	RecordedAt  time.Time

	ItemRef  string
	Ontology OntologyID
	Outcome  Outcome
	Reason   errs.ReasonCode

	Source Source
	// Confidence is the model's own score, as a canonical decimal string. It is
	// recorded for review triage and is never a threshold that promotes a
	// suggestion: no score makes an AI classification authoritative, because
	// ADR-0006 §2.6 is about the route, not about the confidence.
	Confidence string
	Confirmed  *Confirmation
}

// Authoritative reports whether this classification may reach a fiscal path.
//
// This is the enforcement point for ADR-0006 §2.6. Note what it does not do:
// there is no parameter, no policy argument and no confidence threshold, so
// there is nothing a caller can pass to make an unconfirmed suggestion pass.
func (c Classification) Authoritative() bool {
	if c.Outcome != OutcomeClassified {
		return false
	}
	switch c.Source {
	case SourceRule, SourceHuman:
		return true
	case SourceAISuggested:
		// A human confirmation converts a suggestion into something usable, and
		// the confirmation names who made it — which is the audit trail that
		// makes the conversion reviewable rather than a flag someone set.
		return c.Confirmed != nil
	}
	return false
}

// Validate refuses a classification that cannot be stored coherently.
func (c Classification) Validate() error {
	switch c.Source {
	case SourceRule, SourceAISuggested, SourceHuman:
	default:
		return fmt.Errorf("classification: unknown source %q", c.Source)
	}
	switch c.Outcome {
	case OutcomeClassified, OutcomeAmbiguous, OutcomeUnsupported, OutcomeReviewRequired:
	default:
		return fmt.Errorf("classification: unknown outcome %q", c.Outcome)
	}
	if c.Outcome == OutcomeClassified {
		if err := c.Ontology.Validate(); err != nil {
			return err
		}
	}
	if c.Outcome != OutcomeClassified && c.Reason == "" {
		// A refusal has to say why, or it is an outcome nobody can act on.
		return fmt.Errorf("classification: outcome %s names no reason code", c.Outcome)
	}
	if c.Reason != "" && !errs.Registered(c.Reason) {
		return fmt.Errorf("classification: reason code %q is not registered", c.Reason)
	}
	if c.Confirmed != nil {
		if c.Confirmed.By.IsZero() || c.Confirmed.At.IsZero() {
			return fmt.Errorf("classification: confirmation names no user or no time")
		}
		if c.Source != SourceAISuggested {
			// Confirming a rule-derived classification is meaningless and would
			// muddy the audit trail of what was actually reviewed.
			return fmt.Errorf("classification: only an AI suggestion is confirmed, not a %s classification", c.Source)
		}
	}
	return nil
}

// Confirm records a human accepting an AI suggestion.
//
// It returns a new value rather than mutating: under ADR-0003 §2.1 a
// confirmation is a new row linked by Supersedes, not an update, and returning
// a value keeps the caller from accidentally writing the old one.
func (c Classification) Confirm(by id.UserID, at time.Time, newID id.ClassificationID) (Classification, error) {
	if c.Source != SourceAISuggested {
		return Classification{}, fmt.Errorf("classification: %s classifications are not confirmed", c.Source)
	}
	if c.Confirmed != nil {
		return Classification{}, fmt.Errorf("classification: already confirmed")
	}
	prior := c.ID
	next := c
	next.ID = newID
	next.Supersedes = &prior
	next.RecordedAt = at.UTC()
	next.Confirmed = &Confirmation{By: by, At: at.UTC()}
	return next, nil
}

// Canonical renders a classification for the evidence record.
func (c Classification) Canonical() canonical.Value {
	confirmed := canonical.Absent()
	if c.Confirmed != nil {
		confirmed = canonical.Object(
			canonical.F("by", canonical.String(c.Confirmed.By.String())),
			canonical.F("at", canonical.Time(c.Confirmed.At)),
		)
	}
	return canonical.Object(
		canonical.F("itemRef", canonical.String(c.ItemRef)),
		canonical.F("ontology", canonical.OptString(string(c.Ontology))),
		canonical.F("outcome", canonical.String(string(c.Outcome))),
		canonical.F("reason", canonical.OptString(string(c.Reason))),
		canonical.F("source", canonical.String(string(c.Source))),
		canonical.F("confidence", canonical.OptString(c.Confidence)),
		canonical.F("confirmed", confirmed),
		canonical.F("authoritative", canonical.Bool(c.Authoritative())),
	)
}
