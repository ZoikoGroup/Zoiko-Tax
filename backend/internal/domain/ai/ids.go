// Package ai holds the AI plane's advisory record types: AiSuggestion,
// AiExtraction and AiClassificationProposal.
//
// It implements ADR-0006 §2.6. There is no function anywhere in this package,
// or anywhere in the codebase, that converts one of these types into a
// fiscal type. That is not an oversight to fix later — internal/domain/fiscal
// is forbidden from importing this package at all (depguard, CI-blocking),
// and this package must never import internal/domain/fiscal either. The only
// route from a suggestion to an authoritative decision is a human review
// workflow that constructs the fiscal record from reviewed inputs.
package ai

import "fmt"

// AiUseCase names a registered AI use case, e.g. "classification-review" or
// "rule-draft-assistant". The Gateway refuses any request whose use case it
// does not recognise (ADR-0006 §2.5), so this is deliberately an open string
// type rather than a closed enum here — the registry of valid values lives
// with the Gateway, not with this package.
type AiUseCase string

// RiskTier is the T0-T4 risk classification carried on every call across the
// Go/Python boundary (ADR-0006 §2.5).
type RiskTier string

// The five risk tiers named in ADR-0006 §2.5.
const (
	RiskTierT0 RiskTier = "T0"
	RiskTierT1 RiskTier = "T1"
	RiskTierT2 RiskTier = "T2"
	RiskTierT3 RiskTier = "T3"
	RiskTierT4 RiskTier = "T4"
)

func (t RiskTier) valid() bool {
	switch t {
	case RiskTierT0, RiskTierT1, RiskTierT2, RiskTierT3, RiskTierT4:
		return true
	}
	return false
}

// AuthorityOutcome is the A0-A5 authority outcome carried on every call
// across the Go/Python boundary (ADR-0006 §2.5). The Gateway refuses an A5
// action regardless of what any model or tool prompt says.
type AuthorityOutcome string

// The six authority outcomes named in ADR-0006 §2.5. The Gateway refuses an
// A5 action regardless of what any model or tool prompt says.
const (
	AuthorityOutcomeA0 AuthorityOutcome = "A0"
	AuthorityOutcomeA1 AuthorityOutcome = "A1"
	AuthorityOutcomeA2 AuthorityOutcome = "A2"
	AuthorityOutcomeA3 AuthorityOutcome = "A3"
	AuthorityOutcomeA4 AuthorityOutcome = "A4"
	AuthorityOutcomeA5 AuthorityOutcome = "A5"
)

func (a AuthorityOutcome) valid() bool {
	switch a {
	case AuthorityOutcomeA0, AuthorityOutcomeA1, AuthorityOutcomeA2,
		AuthorityOutcomeA3, AuthorityOutcomeA4, AuthorityOutcomeA5:
		return true
	}
	return false
}

// Provenance is the evidenced-transfer metadata ADR-0006 §2.7 requires on
// every crossing of the Go/Python boundary: use case, model profile,
// provider profile, prompt profile, region and data classification.
//
// This is embedded in every record in this package rather than logged
// separately, so a record is never observed without knowing how it was
// produced.
type Provenance struct {
	UseCase          AiUseCase
	ModelProfile     string
	ProviderProfile  string
	PromptProfile    string
	Region           string
	DataClass        string
	RiskTier         RiskTier
	AuthorityOutcome AuthorityOutcome
	AiTrainVersion   string // the AI release-train version, ADR-0006 §2.7 / ADR-0015 §2.6
}

func (p Provenance) validate() error {
	if p.UseCase == "" {
		return fmt.Errorf("ai: empty use case")
	}
	if !p.RiskTier.valid() {
		return fmt.Errorf("ai: unknown risk tier %q", p.RiskTier)
	}
	if !p.AuthorityOutcome.valid() {
		return fmt.Errorf("ai: unknown authority outcome %q", p.AuthorityOutcome)
	}
	if p.Region == "" {
		return fmt.Errorf("ai: empty region")
	}
	if p.AiTrainVersion == "" {
		return fmt.Errorf("ai: empty AI train version")
	}
	return nil
}

// AiSuggestionID identifies an AiSuggestion. A struct wrapper, not an alias,
// per ADR-0012 §2.2 — passing an AiExtractionID where an AiSuggestionID is
// expected must be a compile error.
//
// The underlying value is an opaque string produced by
// internal/platform/idgen (ADR-0012 §2.3) once that package exists. This
// type does not generate its own identifiers.
type AiSuggestionID struct{ v string }

// NewAiSuggestionID wraps an already-generated identifier.
func NewAiSuggestionID(v string) (AiSuggestionID, error) {
	if v == "" {
		return AiSuggestionID{}, fmt.Errorf("ai: empty AiSuggestionID")
	}
	return AiSuggestionID{v: v}, nil
}

// String returns the underlying identifier.
func (id AiSuggestionID) String() string { return id.v }

// AiExtractionID identifies an AiExtraction. See AiSuggestionID for the
// wrapping rationale.
type AiExtractionID struct{ v string }

// NewAiExtractionID wraps an already-generated identifier.
func NewAiExtractionID(v string) (AiExtractionID, error) {
	if v == "" {
		return AiExtractionID{}, fmt.Errorf("ai: empty AiExtractionID")
	}
	return AiExtractionID{v: v}, nil
}

// String returns the underlying identifier.
func (id AiExtractionID) String() string { return id.v }

// AiClassificationProposalID identifies an AiClassificationProposal. See
// AiSuggestionID for the wrapping rationale.
type AiClassificationProposalID struct{ v string }

// NewAiClassificationProposalID wraps an already-generated identifier.
func NewAiClassificationProposalID(v string) (AiClassificationProposalID, error) {
	if v == "" {
		return AiClassificationProposalID{}, fmt.Errorf("ai: empty AiClassificationProposalID")
	}
	return AiClassificationProposalID{v: v}, nil
}

// String returns the underlying identifier.
func (id AiClassificationProposalID) String() string { return id.v }
