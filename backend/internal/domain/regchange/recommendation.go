package regchange

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Review recommendations (Sidebar Navigation Architecture WF-02 "Specialist
// rule review": Request changes • Recommend • Independent review; "No direct
// release-pack publication"; ZTAX-AIGOV-001 §4, §12, §14).
//
// A recommendation is drafted on one assessment, bound to its digest. A
// specialist recommends it, and a different person — neither its author
// nor its recommender — independently reviews it: ACCEPTED, REJECTED or
// CHANGES_REQUESTED. An accepted recommendation yields an opaque hand-off
// reference and nothing else: no rule, pack, content or tax result changes
// here. A rule-approval workflow may later name the recommendation by its
// id or hand-off reference.
//
// An AI-assisted draft records the model's provenance and its original text
// as immutable evidence; the rationale a person adopts or edits is theirs
// (AIGOV-001 §12 "Reviewer edits become human-authored decisions"). AI is
// at most A1 PROPOSE here and never counts as one of the two people.

// Action is what a recommendation recommends.
type Action string

// The actions.
const (
	ActionUpdateRule                Action = "UPDATE_RULE"
	ActionAddRule                   Action = "ADD_RULE"
	ActionRetireRule                Action = "RETIRE_RULE"
	ActionNotifyCustomers           Action = "NOTIFY_CUSTOMERS"
	ActionRetrospectiveReassessment Action = "RETROSPECTIVE_REASSESSMENT"
	ActionMonitor                   Action = "MONITOR"
	ActionNoAction                  Action = "NO_ACTION"
)

// Actions is every action.
var Actions = []Action{ActionUpdateRule, ActionAddRule, ActionRetireRule, ActionNotifyCustomers,
	ActionRetrospectiveReassessment, ActionMonitor, ActionNoAction}

// Valid reports whether the action is one of these.
func (a Action) Valid() bool { return slices.Contains(Actions, a) }

// Origin is who drafted a recommendation.
type Origin string

// The origins.
const (
	OriginHuman      Origin = "HUMAN"
	OriginAIAssisted Origin = "AI_ASSISTED"
)

// AIProvenance is what AIGOV-001 §14 requires recorded of an AI-assisted
// draft: the use case, the model, provider and prompt profiles, the AI
// release train, the authority outcome and the invocation, and the model's
// original text, kept unchanged as evidence.
type AIProvenance struct {
	UseCase          string
	ModelProfile     string
	ProviderProfile  string
	PromptProfile    string
	AiTrainVersion   string
	AuthorityOutcome AuthorityOutcome
	InvocationID     string
	Draft            string
}

// Validate refuses incomplete provenance and any authority above A1
// PROPOSE: a recommendation is a proposal a person adopts (AIGOV-001 §4).
func (p AIProvenance) Validate() error {
	for _, f := range []struct{ field, value string }{
		{"ai.useCase", p.UseCase}, {"ai.modelProfile", p.ModelProfile}, {"ai.providerProfile", p.ProviderProfile},
		{"ai.promptProfile", p.PromptProfile}, {"ai.aiTrainVersion", p.AiTrainVersion},
	} {
		if err := text(f.field, f.value, true, MaxText); err != nil {
			return err
		}
	}
	if err := text("ai.invocationId", p.InvocationID, false, MaxText); err != nil {
		return err
	}
	if p.AuthorityOutcome != AuthorityPropose {
		return errs.Invalid("ai.authorityOutcome", errs.ReasonInvalidValue,
			"An AI-assisted recommendation is a proposal: authority A1 PROPOSE. AI never approves, publishes or promotes content.")
	}
	if strings.TrimSpace(p.Draft) == "" || len(p.Draft) > MaxSummary {
		return errs.Invalid("ai.draft", errs.ReasonMissingField, fmt.Sprintf("The model's original text is recorded as evidence, 1 to %d characters.", MaxSummary))
	}
	return nil
}

// RecStatus is where a recommendation stands.
type RecStatus string

// The statuses. ACCEPTED, REJECTED and WITHDRAWN are final.
const (
	RecDraft            RecStatus = "DRAFT"
	RecRecommended      RecStatus = "RECOMMENDED"
	RecChangesRequested RecStatus = "CHANGES_REQUESTED"
	RecAccepted         RecStatus = "ACCEPTED"
	RecRejected         RecStatus = "REJECTED"
	RecWithdrawn        RecStatus = "WITHDRAWN"
)

// RecStatuses is every status.
var RecStatuses = []RecStatus{RecDraft, RecRecommended, RecChangesRequested, RecAccepted, RecRejected, RecWithdrawn}

// Valid reports whether the status is one of these.
func (s RecStatus) Valid() bool { return slices.Contains(RecStatuses, s) }

// Final reports whether nothing more happens to a recommendation.
func (s RecStatus) Final() bool { return s == RecAccepted || s == RecRejected || s == RecWithdrawn }

// RecEventKind is what happened to a recommendation.
type RecEventKind string

// The events. DRAFTED is always the first.
const (
	RecEventDrafted          RecEventKind = "DRAFTED"
	RecEventRevised          RecEventKind = "REVISED"
	RecEventRecommended      RecEventKind = "RECOMMENDED"
	RecEventChangesRequested RecEventKind = "CHANGES_REQUESTED"
	RecEventAccepted         RecEventKind = "ACCEPTED"
	RecEventRejected         RecEventKind = "REJECTED"
	RecEventWithdrawn        RecEventKind = "WITHDRAWN"
)

// Decision is an independent reviewer's outcome.
type Decision string

// The decisions.
const (
	DecisionAccept         Decision = "ACCEPT"
	DecisionReject         Decision = "REJECT"
	DecisionRequestChanges Decision = "REQUEST_CHANGES"
)

// Target is one impacted item of the assessment a recommendation is about.
type Target struct {
	Kind ItemKind
	Ref  string
}

// Content is what a recommendation says: the action, the rationale and the
// items it is about.
type Content struct {
	Action    Action
	Rationale string
	Targets   []Target
}

// Normalize trims the rationale and orders the targets.
func (c Content) Normalize() Content {
	c.Rationale = strings.TrimSpace(c.Rationale)
	c.Targets = slices.Clone(c.Targets)
	for i := range c.Targets {
		c.Targets[i].Ref = strings.TrimSpace(c.Targets[i].Ref)
	}
	slices.SortFunc(c.Targets, func(a, b Target) int {
		return strings.Compare(string(a.Kind)+"\x00"+a.Ref, string(b.Kind)+"\x00"+b.Ref)
	})
	c.Targets = slices.Compact(c.Targets)
	return c
}

// Check refuses content the assessment does not support: an unknown action,
// no rationale, a target the assessment did not find, or an action whose
// targets are of the wrong kind.
func (c Content) Check(a Assessment) error {
	if !c.Action.Valid() {
		return errs.Invalid("action", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a recommendation action.", c.Action))
	}
	if err := reasonText("rationale", c.Rationale); err != nil {
		return err
	}
	if len(c.Targets) > 200 {
		return errs.Invalid("targets", errs.ReasonInvalidValue, "A recommendation names at most 200 items.")
	}
	content, tenant, retro := 0, 0, 0
	for _, t := range c.Targets {
		if !a.HasItem(t.Kind, t.Ref) {
			return errs.Invalid("targets", errs.ReasonInvalidValue,
				fmt.Sprintf("%s %s is not an item assessment %s found. A recommendation is about what its assessment found.", t.Kind, t.Ref, a.ID))
		}
		if t.Kind.Content() {
			content++
		} else {
			tenant++
		}
		for _, it := range a.Items {
			if it.Kind == t.Kind && it.Ref == t.Ref && it.Retrospective {
				retro++
			}
		}
	}
	switch c.Action {
	case ActionUpdateRule, ActionRetireRule:
		if content == 0 || tenant > 0 || slices.ContainsFunc(c.Targets, func(t Target) bool { return t.Kind == ItemCoverageGap }) {
			return errs.Invalid("targets", errs.ReasonInvalidValue, fmt.Sprintf("%s names the content components it is about, and only those.", c.Action))
		}
	case ActionAddRule:
		if content == 0 || tenant > 0 {
			return errs.Invalid("targets", errs.ReasonInvalidValue, "ADD_RULE names the coverage gap or the components it adds beside, and only those.")
		}
	case ActionNotifyCustomers:
		if tenant == 0 {
			return errs.Invalid("targets", errs.ReasonInvalidValue, "NOTIFY_CUSTOMERS names the registrations or obligations that are exposed.")
		}
	case ActionRetrospectiveReassessment:
		if !a.Retroactive || retro == 0 {
			return errs.Invalid("targets", errs.ReasonInvalidValue,
				"A retrospective reassessment is about the obligation periods a retroactive change reaches back into.")
		}
	}
	return nil
}

// RecEvent is one appended act on a recommendation.
type RecEvent struct {
	Seq     int
	Kind    RecEventKind
	Status  RecStatus
	Content *Content // on DRAFTED and REVISED
	Reason  string
	// HandoffRef is the opaque reference an ACCEPTED event records.
	HandoffRef string
	RecordedAt time.Time
	RecordedBy id.UserID
}

// Recommendation is one review recommendation, folded with its events.
type Recommendation struct {
	ID       id.ReviewRecommendationID
	TenantID id.TenantID
	Change   id.RegulatoryChangeID

	// Assessment and AssessmentDigest bind the recommendation to the exact
	// findings it was made on.
	Assessment       id.ImpactAssessmentID
	AssessmentDigest canonical.Digest

	Origin Origin
	AI     *AIProvenance

	IntakeKey  string
	IntakeHash canonical.Digest
	CreatedAt  time.Time
	CreatedBy  id.UserID

	Status  RecStatus
	History []RecEvent
}

// Version is the number of events recorded.
func (r Recommendation) Version() int { return len(r.History) }

// Validate refuses a recommendation that cannot be recorded.
func (r Recommendation) Validate() error {
	switch r.Origin {
	case OriginHuman:
		if r.AI != nil {
			return errs.Invalid("ai", errs.ReasonInvalidValue, "A HUMAN recommendation carries no AI provenance.")
		}
	case OriginAIAssisted:
		if r.AI == nil {
			return errs.Invalid("ai", errs.ReasonMissingField, "An AI_ASSISTED recommendation records the model's provenance (AIGOV-001 §14).")
		}
		if err := r.AI.Validate(); err != nil {
			return err
		}
	default:
		return errs.Invalid("origin", errs.ReasonInvalidValue, "A recommendation's origin is HUMAN or AI_ASSISTED.")
	}
	return nil
}

// Payload is the canonical document the intake hash covers.
func (r Recommendation) Payload(c Content) canonical.Value {
	tv := make([]canonical.Value, len(c.Targets))
	for i, t := range c.Targets {
		tv[i] = canonical.Object(canonical.F("kind", canonical.String(string(t.Kind))), canonical.F("ref", canonical.String(t.Ref)))
	}
	aiv := canonical.Absent()
	if r.AI != nil {
		aiv = canonical.Object(
			canonical.F("useCase", canonical.String(r.AI.UseCase)),
			canonical.F("modelProfile", canonical.String(r.AI.ModelProfile)),
			canonical.F("providerProfile", canonical.String(r.AI.ProviderProfile)),
			canonical.F("promptProfile", canonical.String(r.AI.PromptProfile)),
			canonical.F("aiTrainVersion", canonical.String(r.AI.AiTrainVersion)),
			canonical.F("authorityOutcome", canonical.String(string(r.AI.AuthorityOutcome))),
			canonical.F("invocationId", canonical.OptString(r.AI.InvocationID)),
			canonical.F("draft", canonical.String(r.AI.Draft)),
		)
	}
	return canonical.Object(
		canonical.F("assessmentId", canonical.String(r.Assessment.String())),
		canonical.F("origin", canonical.String(string(r.Origin))),
		canonical.F("ai", aiv),
		canonical.F("action", canonical.String(string(c.Action))),
		canonical.F("rationale", canonical.String(c.Rationale)),
		canonical.F("targets", canonical.Array(tv...)),
	)
}

// Draft is a new recommendation's first event.
func Draft(r Recommendation, c Content, a Assessment, by id.UserID, at time.Time) (RecEvent, error) {
	if err := r.Validate(); err != nil {
		return RecEvent{}, err
	}
	c = c.Normalize()
	if err := c.Check(a); err != nil {
		return RecEvent{}, err
	}
	return RecEvent{Seq: 1, Kind: RecEventDrafted, Status: RecDraft, Content: &c, RecordedAt: at.UTC(), RecordedBy: by}, nil
}

// FoldRecommendation applies a recommendation's events in order.
func FoldRecommendation(r Recommendation, events []RecEvent) (Recommendation, error) {
	if len(events) == 0 || events[0].Kind != RecEventDrafted || events[0].Seq != 1 || events[0].Content == nil {
		return Recommendation{}, fmt.Errorf("regchange: a recommendation's history starts with its DRAFTED event")
	}
	r.Status = RecDraft
	for i, e := range events {
		if e.Seq != i+1 {
			return Recommendation{}, fmt.Errorf("regchange: recommendation event %d is out of sequence", e.Seq)
		}
		if i > 0 && !recFolds(r.Status, e) {
			return Recommendation{}, fmt.Errorf("regchange: %s from %s to %s", e.Kind, r.Status, e.Status)
		}
		r.Status = e.Status
	}
	r.History = slices.Clone(events)
	return r, nil
}

func recFolds(from RecStatus, e RecEvent) bool {
	switch e.Kind {
	case RecEventRevised:
		return (from == RecDraft || from == RecChangesRequested) && e.Status == RecDraft && e.Content != nil
	case RecEventRecommended:
		return from == RecDraft && e.Status == RecRecommended
	case RecEventChangesRequested:
		return from == RecRecommended && e.Status == RecChangesRequested
	case RecEventAccepted:
		return from == RecRecommended && e.Status == RecAccepted && e.HandoffRef != ""
	case RecEventRejected:
		return from == RecRecommended && e.Status == RecRejected
	case RecEventWithdrawn:
		return !from.Final() && e.Status == RecWithdrawn
	}
	return false
}

func (r Recommendation) last(kinds ...RecEventKind) (RecEvent, bool) {
	for i := len(r.History) - 1; i >= 0; i-- {
		if slices.Contains(kinds, r.History[i].Kind) {
			return r.History[i], true
		}
	}
	return RecEvent{}, false
}

// Current is what the recommendation says now, and who last wrote it.
func (r Recommendation) Current() (Content, id.UserID) {
	e, _ := r.last(RecEventDrafted, RecEventRevised)
	if e.Content == nil {
		return Content{}, id.UserID{}
	}
	return *e.Content, e.RecordedBy
}

// Recommender is who recommended it, and false before it is recommended.
func (r Recommendation) Recommender() (id.UserID, bool) {
	if r.Status != RecRecommended && r.Status != RecAccepted && r.Status != RecRejected {
		return id.UserID{}, false
	}
	e, ok := r.last(RecEventRecommended)
	return e.RecordedBy, ok
}

// Review is the independent review, and false before one is recorded.
func (r Recommendation) Review() (RecEvent, bool) {
	if r.Status != RecAccepted && r.Status != RecRejected {
		return RecEvent{}, false
	}
	return r.History[len(r.History)-1], true
}

// HandoffRef is the accepted recommendation's hand-off reference, and false
// unless it was accepted.
func (r Recommendation) HandoffRef() (string, bool) {
	if r.Status != RecAccepted {
		return "", false
	}
	return r.History[len(r.History)-1].HandoffRef, true
}

func (r Recommendation) next(kind RecEventKind, to RecStatus, reason string, by id.UserID, at time.Time) RecEvent {
	return RecEvent{Seq: len(r.History) + 1, Kind: kind, Status: to, Reason: reason, RecordedAt: at.UTC(), RecordedBy: by}
}

func (r Recommendation) checkVersion(version int) error {
	if version != r.Version() {
		return errs.New(errs.CategoryConflict, ReasonVersionConflict,
			fmt.Sprintf("The recommendation is at version %d; version %d was read. Re-read it and act on the current version.", r.Version(), version))
	}
	return nil
}

// checkCurrent refuses an act on a recommendation whose assessment is no
// longer the change's current one.
func (r Recommendation) checkCurrent(current id.ImpactAssessmentID) error {
	if current != r.Assessment {
		return errs.New(errs.CategoryConflict, ReasonAssessmentSuperseded,
			fmt.Sprintf("The change has been reassessed since this recommendation was made on assessment %s.", r.Assessment))
	}
	return nil
}

// Revise replaces what a DRAFT or CHANGES_REQUESTED recommendation says.
// The earlier text stays in the history.
func (r Recommendation) Revise(c Content, a Assessment, version int, by id.UserID, at time.Time) (RecEvent, error) {
	if err := r.checkVersion(version); err != nil {
		return RecEvent{}, err
	}
	if r.Status != RecDraft && r.Status != RecChangesRequested {
		return RecEvent{}, invalidMove("recommendation", string(r.Status), string(RecDraft))
	}
	if a.ID != r.Assessment {
		return RecEvent{}, errs.New(errs.CategoryInternal, errs.ReasonInternal, "A recommendation is revised against its own assessment.")
	}
	c = c.Normalize()
	if err := c.Check(a); err != nil {
		return RecEvent{}, err
	}
	e := r.next(RecEventRevised, RecDraft, "", by, at)
	e.Content = &c
	return e, nil
}

// Recommend is a specialist's recommendation of a DRAFT, on the change's
// current assessment.
func (r Recommendation) Recommend(reason string, version int, current id.ImpactAssessmentID, by id.UserID, at time.Time) (RecEvent, error) {
	reason = strings.TrimSpace(reason)
	if err := r.checkVersion(version); err != nil {
		return RecEvent{}, err
	}
	if r.Status != RecDraft {
		return RecEvent{}, invalidMove("recommendation", string(r.Status), string(RecRecommended))
	}
	if err := r.checkCurrent(current); err != nil {
		return RecEvent{}, err
	}
	if err := reasonText("reason", reason); err != nil {
		return RecEvent{}, err
	}
	return r.next(RecEventRecommended, RecRecommended, reason, by, at), nil
}

// HandoffRefFor is the opaque reference an accepted recommendation hands a
// later workflow: the recommendation and the assessment it was bound to.
func HandoffRefFor(r Recommendation) string {
	d := strings.TrimPrefix(r.AssessmentDigest.String(), "zt1:")
	if len(d) > 16 {
		d = d[:16]
	}
	return "ztx-rcr:" + r.ID.String() + ":" + d
}

// Authored reports whether a person wrote any version of the
// recommendation: its draft, recording an AI draft included, or a revision.
func (r Recommendation) Authored(by id.UserID) bool {
	return slices.ContainsFunc(r.History, func(e RecEvent) bool {
		return (e.Kind == RecEventDrafted || e.Kind == RecEventRevised) && e.RecordedBy == by
	})
}

// Decide is the independent review of a RECOMMENDED recommendation: by
// someone who neither wrote nor recommended it, on its exact version and
// the change's current assessment. ACCEPT records a hand-off reference;
// it publishes, approves or changes nothing else.
func (r Recommendation) Decide(d Decision, reason string, version int, current id.ImpactAssessmentID, by id.UserID, at time.Time) (RecEvent, error) {
	reason = strings.TrimSpace(reason)
	if err := r.checkVersion(version); err != nil {
		return RecEvent{}, err
	}
	if r.Status != RecRecommended {
		return RecEvent{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only a RECOMMENDED recommendation is reviewed; this one is %s.", r.Status))
	}
	if rec, _ := r.Recommender(); rec == by {
		return RecEvent{}, makerChecker("The person who recommended this cannot also review it. An independent reviewer must.")
	}
	if r.Authored(by) {
		return RecEvent{}, makerChecker("A person who wrote any version of this recommendation cannot also review it. An independent reviewer must.")
	}
	if err := r.checkCurrent(current); err != nil {
		return RecEvent{}, err
	}
	if err := reasonText("reason", reason); err != nil {
		return RecEvent{}, err
	}
	switch d {
	case DecisionAccept:
		e := r.next(RecEventAccepted, RecAccepted, reason, by, at)
		e.HandoffRef = HandoffRefFor(r)
		return e, nil
	case DecisionReject:
		return r.next(RecEventRejected, RecRejected, reason, by, at), nil
	case DecisionRequestChanges:
		return r.next(RecEventChangesRequested, RecChangesRequested, reason, by, at), nil
	}
	return RecEvent{}, errs.Invalid("decision", errs.ReasonInvalidValue, "A review decides ACCEPT, REJECT or REQUEST_CHANGES.")
}

// Withdraw withdraws an open recommendation, with the reason.
func (r Recommendation) Withdraw(reason string, version int, by id.UserID, at time.Time) (RecEvent, error) {
	reason = strings.TrimSpace(reason)
	if err := r.checkVersion(version); err != nil {
		return RecEvent{}, err
	}
	if r.Status.Final() {
		return RecEvent{}, invalidMove("recommendation", string(r.Status), string(RecWithdrawn))
	}
	if err := reasonText("reason", reason); err != nil {
		return RecEvent{}, err
	}
	return r.next(RecEventWithdrawn, RecWithdrawn, reason, by, at), nil
}

// RecFilter narrows a listing. Empty fields do not filter.
type RecFilter struct {
	Status RecStatus
	Change id.RegulatoryChangeID
	Origin Origin
}

// Match reports whether a recommendation passes the filter.
func (f RecFilter) Match(r Recommendation) bool {
	return (f.Status == "" || r.Status == f.Status) && (f.Change.IsZero() || r.Change == f.Change) &&
		(f.Origin == "" || r.Origin == f.Origin)
}

// AuthorityOutcome is the AIGOV-001 §4 authority level an AI draft was made
// at, as the AI Gateway names it (A0-A5). It is a string here rather than
// the domain/ai type: domain packages do not depend on domain/ai (archtest
// TestDomainNeverDependsOnAI).
type AuthorityOutcome string

// AuthorityPropose is A1 PROPOSE, the only authority a recommendation's AI
// draft may carry.
const AuthorityPropose AuthorityOutcome = "A1"
