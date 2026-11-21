package app

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ExplainObligationUseCase is the governed AI use case that explains one
// obligation row. It is registered in the AI-train registry both halves of the
// Gateway boundary load.
const ExplainObligationUseCase ai.AiUseCase = "explain-obligation-row"

// ObligationReader reads one obligation under the caller's tenant.
// *DeterminationService is the implementation: the explanation reads a row the
// way GET /v1/obligations/{obligationId} does, and adds no store method.
type ObligationReader interface {
	Obligation(ctx context.Context, obligationID id.ObligationID) (ObligationView, error)
}

// ExplanationStatus is what the AI plane contributed to one obligation.
type ExplanationStatus string

// The statuses.
const (
	// ExplanationAnswered carries an explanation that the Gateway passed and
	// that cites only the row that was sent.
	ExplanationAnswered ExplanationStatus = "ANSWERED"
	// ExplanationAbstained carries none: the Gateway answered and set the
	// explanation aside.
	ExplanationAbstained ExplanationStatus = "ABSTAINED"
	// ExplanationReviewRequired carries none: the AI plane could not help, or
	// its answer cannot be used. A person reads the obligation without it.
	ExplanationReviewRequired ExplanationStatus = "REVIEW_REQUIRED"
)

// ExplanationOutcome is the answer for one obligation. It is advisory by
// construction: there is no field that could make it authoritative.
type ExplanationOutcome struct {
	ObligationID id.ObligationID
	Status       ExplanationStatus
	// ReviewReason is a fixed code of ours, set unless Status is ANSWERED. It
	// never holds text a model wrote.
	ReviewReason errs.ReasonCode
	// Explanation is set only when Status is ANSWERED.
	Explanation string
	// CitedIDs are the ids the explanation cites, set only when Status is
	// ANSWERED. Every one is an id this service sent.
	CitedIDs []string
	// ValidatorVersion is the answer check's version as the Gateway reported
	// it, kept only when it has the shape of a version.
	ValidatorVersion string
	// Provenance is set whenever the Gateway answered.
	Provenance    ai.Provenance
	HasProvenance bool
}

// explanationField is one field of the obligation row that is sent to the
// model, with the privacy class of that field in the contract (the
// x-ztax-privacy tag of the Obligation schema, or of the schema it refers to).
type explanationField struct {
	Name  string
	Class privacy.Class
}

// explanationFields is the one table of what the explainer is sent. Nothing
// else of the row goes: not the legal entity, the content digest, the
// responsibility decision or who recorded it, because an explanation of a
// figure needs none of them. A field is added here, with its class, or it is
// not sent; the declared data classes below are taken from this table.
var explanationFields = []explanationField{
	{"id", privacy.P0},
	{"type", privacy.P0},
	{"duty", privacy.P0},
	{"jurisdiction", privacy.P0},
	{"authority", privacy.P0},
	{"periodStart", privacy.P0},
	{"periodEnd", privacy.P0},
	{"dueDate", privacy.P0},
	{"timezone", privacy.P0},
	{"status", privacy.P0},
	{"effectiveStatus", privacy.P0},
	{"assessedAmount", privacy.P5}, // the assessed amount: fiscal data
	{"currency", privacy.P0},
}

// ExplanationFieldClasses returns each field the explainer is sent with its
// class.
func ExplanationFieldClasses() map[string]privacy.Class {
	out := make(map[string]privacy.Class, len(explanationFields))
	for _, f := range explanationFields {
		out[f.Name] = f.Class
	}
	return out
}

// ExplanationDataClasses are the classes the call declares: every class in the
// table, in PRIV-001 order, whether or not this particular row has a value for
// each field. An obligation not yet assessed has no amount, but the route can
// carry one, so it always declares what it can carry. Declaring less would
// send fiscal data under a class the use case may be approved for.
func ExplanationDataClasses() []privacy.Class {
	var out []privacy.Class
	for _, c := range privacy.Classes {
		for _, f := range explanationFields {
			if f.Class == c {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// ObligationExplanationService asks the AI plane to explain one obligation.
//
// Everything it returns is advisory. The answer check runs in the Gateway; this
// service only reads the checked answer and fails closed, so anything it does
// not recognise gives no text.
type ObligationExplanationService struct {
	models      port.ModelGateway
	obligations ObligationReader
}

// NewObligationExplanationService wires the service.
func NewObligationExplanationService(models port.ModelGateway, obligations ObligationReader) *ObligationExplanationService {
	return &ObligationExplanationService{models: models, obligations: obligations}
}

// Explain reads the obligation under the caller's tenant and asks the Gateway
// to explain it. The use case, authority and risk tier are fixed here, as for
// classification, at the values the registry carries.
//
// When the AI plane cannot help, the outcome is REVIEW_REQUIRED with the
// reason, not an error. A governance refusal is not among those cases: it is
// returned as the Gateway's error.
func (s *ObligationExplanationService) Explain(ctx context.Context, obligationID id.ObligationID) (ExplanationOutcome, error) {
	sc, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst)
	if err != nil {
		return ExplanationOutcome{}, err
	}
	view, err := s.obligations.Obligation(ctx, obligationID)
	if err != nil {
		return ExplanationOutcome{}, err
	}
	// The store reads under the session's tenant already; this holds even if a
	// reader were wired that did not.
	if view.TenantID != sc.Tenant() {
		return ExplanationOutcome{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound,
			"The referenced resource does not exist, or is outside the caller's tenant.")
	}
	input, err := explanationInput(view)
	if err != nil {
		return ExplanationOutcome{}, internal(err, "The explanation request could not be encoded.")
	}
	ext, err := s.models.Extract(ctx, ai.Invocation{
		UseCase:          ExplainObligationUseCase,
		AuthorityOutcome: ai.AuthorityOutcome("A1"),
		RiskTier:         ai.RiskTier("T1"),
		DataClasses:      ExplanationDataClasses(),
		SubjectRef:       view.ID.String(),
		Input:            input,
	})
	if err != nil {
		if reason := errs.ReasonOf(err); slices.Contains(reviewWithoutAI, reason) {
			return ExplanationOutcome{ObligationID: view.ID, Status: ExplanationReviewRequired, ReviewReason: reason}, nil
		}
		return ExplanationOutcome{}, err
	}

	a := readExplanationAnswer(ext.Fields(), map[string]bool{view.ID.String(): true})
	out := ExplanationOutcome{
		ObligationID: view.ID, Status: a.status, ReviewReason: a.reason,
		Explanation: a.explanation, CitedIDs: a.cited, ValidatorVersion: a.validator,
		Provenance: ext.Provenance(), HasProvenance: true,
	}
	return out, nil
}

// explanationInput is the canonical JSON the model is given: the one row, as
// the fields in explanationFields. Dates are civil dates and the amount is its
// canonical decimal string.
func explanationInput(v ObligationView) ([]byte, error) {
	o := v.Obligation
	values := map[string]string{
		"id": o.ID.String(), "type": o.Type, "duty": string(o.Duty),
		"jurisdiction": o.JurisdictionID, "authority": o.Authority,
		"periodStart": o.Period.Start.Format(time.DateOnly),
		"periodEnd":   o.Period.End.Format(time.DateOnly),
		"dueDate":     o.Period.Due.Format(time.DateOnly),
		"timezone":    o.Timezone, "status": string(o.Status), "effectiveStatus": string(v.EffectiveStatus),
	}
	if o.Assessed != nil {
		values["assessedAmount"] = o.Assessed.CanonicalString()
		values["currency"] = string(o.Assessed.Currency())
	}
	fields := make([]canonical.Field, 0, len(explanationFields))
	for _, f := range explanationFields {
		if val, ok := values[f.Name]; ok {
			fields = append(fields, canonical.F(f.Name, canonical.String(val)))
		}
	}
	return canonical.Encode(canonical.Object(
		canonical.F("kind", canonical.String("obligation-row")),
		canonical.F("rows", canonical.Array(canonical.Object(fields...))),
	))
}

// The Gateway's reply, as read. This is the one place that knows its shape: the
// reply carries `status` (ANSWERED, ABSTAINED or REFUSED), `reason_code`,
// `cited_ids` (a JSON array of strings), `validator_version` and
// `explanation`. The shape is still being agreed, so a change is made here
// and nowhere else. Only `status`, `cited_ids`, `validator_version` and
// `explanation` are read; `reason_code` and anything else the reply carries
// is ignored, so nothing the model wrote reaches a reason.
const (
	replyStatusAnswered  = "ANSWERED"
	replyStatusAbstained = "ABSTAINED"
	replyStatusRefused   = "REFUSED"

	maxExplanationBytes = 8 * 1024
	maxCitedIDs         = 64
)

var validatorVersionRE = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

type explanationAnswer struct {
	status      ExplanationStatus
	reason      errs.ReasonCode
	explanation string
	cited       []string
	validator   string
}

func unusableAnswer() explanationAnswer {
	return explanationAnswer{status: ExplanationReviewRequired, reason: ai.ReasonExplanationReplyUnusable}
}

// readExplanationAnswer turns the Gateway's fields into an answer. It fails
// closed: text is returned only when the status is exactly ANSWERED, cited_ids
// is a non-empty JSON array of strings, every one is in supplied, and there is
// text. Any other status, or a missing one, gives no text.
func readExplanationAnswer(fields map[string]string, supplied map[string]bool) explanationAnswer {
	switch fields["status"] {
	case replyStatusAbstained:
		return explanationAnswer{status: ExplanationAbstained, reason: ai.ReasonExplanationAbstained}
	case replyStatusRefused:
		return explanationAnswer{status: ExplanationAbstained, reason: ai.ReasonExplanationRefused}
	case replyStatusAnswered:
		// Handled below.
	default:
		return unusableAnswer()
	}
	var cited []string
	if err := json.Unmarshal([]byte(fields["cited_ids"]), &cited); err != nil || len(cited) == 0 || len(cited) > maxCitedIDs {
		return unusableAnswer()
	}
	for _, c := range cited {
		if !supplied[c] {
			return unusableAnswer()
		}
	}
	text := fields["explanation"]
	if strings.TrimSpace(text) == "" || len(text) > maxExplanationBytes {
		return unusableAnswer()
	}
	a := explanationAnswer{status: ExplanationAnswered, explanation: text, cited: cited}
	if v := fields["validator_version"]; validatorVersionRE.MatchString(v) {
		a.validator = v
	}
	return a
}
