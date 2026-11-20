package classification

import (
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// Action is what a reviewer did with an AI classification proposal.
type Action string

// The reviewer actions. The vocabulary is the one the proposal card shows.
const (
	ActionAccept   Action = "ACCEPT"
	ActionReject   Action = "REJECT"
	ActionModify   Action = "MODIFY"
	ActionEscalate Action = "ESCALATE"
)

// Valid reports whether a is one of the four actions.
func (a Action) Valid() bool {
	switch a {
	case ActionAccept, ActionReject, ActionModify, ActionEscalate:
		return true
	}
	return false
}

// The length limits, in characters. They match the contract's maxLength.
const (
	DecisionReasonMax     = 500
	DecisionNoteMax       = 2000
	DecisionSubjectRefMax = 512
	DecisionCodeMax       = 256
)

var evidenceIDShape = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// ReviewDecision is a reviewer's decision on one AI proposal. It is append-only: one
// decision per proposal per tenant. The proposal itself is advisory and is not
// stored, so the record carries what it needs of it. Reason and Note are free
// text: they are never logged and never put in an error message.
type ReviewDecision struct {
	TenantID     id.TenantID
	ProposalID   uuid.UUID
	SubjectRef   string
	ProposedCode string
	Action       Action
	Reason       string
	Note         string
	ModifiedCode string
	// EvidenceID is the Gateway's evidence record id for the proposal's call,
	// when the proposal had one. It is opaque and optional.
	EvidenceID string
	ReviewerID id.UserID
	DecidedAt  time.Time
}

func chars(s string) int { return utf8.RuneCountInString(s) }

func invalid(field, detail string) error {
	return errs.Invalid(field, errs.ReasonInvalidValue, detail)
}

// Validate checks the decision. Messages are fixed and never echo a value.
func (d ReviewDecision) Validate() error {
	if d.TenantID.IsZero() || d.ReviewerID.IsZero() || d.DecidedAt.IsZero() {
		return errs.New(errs.CategoryInternal, errs.ReasonInternal, "The decision has no reviewer, tenant or time.")
	}
	if d.ProposalID == uuid.Nil {
		return invalid("proposalId", "A proposal identifier is required.")
	}
	if d.SubjectRef == "" || chars(d.SubjectRef) > DecisionSubjectRefMax {
		return invalid("subjectRef", "A subject reference of at most 512 characters is required.")
	}
	if chars(d.ProposedCode) > DecisionCodeMax || OntologyID(d.ProposedCode).Validate() != nil {
		return invalid("proposedCode", "The proposed code must be in ontology format.")
	}
	if !d.Action.Valid() {
		return invalid("action", "The action must be ACCEPT, REJECT, MODIFY or ESCALATE.")
	}
	if d.Reason == "" {
		return errs.Invalid("reason", errs.ReasonMissingField, "A reason of at most 500 characters is required.")
	}
	if chars(d.Reason) > DecisionReasonMax {
		return errs.Invalid("reason", errs.ReasonInvalidValue, "A reason can be at most 500 characters.")
	}
	if chars(d.Note) > DecisionNoteMax {
		return invalid("note", "A note can be at most 2000 characters.")
	}
	switch {
	case d.Action == ActionModify && d.ModifiedCode == "":
		return errs.Invalid("modifiedCode", errs.ReasonMissingField, "A modified code is required for MODIFY.")
	case d.Action == ActionModify:
		if chars(d.ModifiedCode) > DecisionCodeMax || OntologyID(d.ModifiedCode).Validate() != nil {
			return invalid("modifiedCode", "The modified code must be in ontology format.")
		}
	case d.ModifiedCode != "":
		return invalid("modifiedCode", "Only MODIFY carries a modified code.")
	}
	if d.EvidenceID != "" && !evidenceIDShape.MatchString(d.EvidenceID) {
		return invalid("evidenceId", "The evidence id must be 1 to 64 letters, digits or dashes.")
	}
	return nil
}
