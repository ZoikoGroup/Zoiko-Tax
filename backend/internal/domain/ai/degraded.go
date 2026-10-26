package ai

import "github.com/zoikogroup/zoikotax/backend/internal/domain/errs"

// The degraded-state reason codes: the AI plane could not contribute, through
// no refusal of governance's. port.ModelGateway promises the first three, and
// a caller acts on them — the work it was assisting goes to a person without
// a suggestion (ADR-0006 §2.4) — so they live here rather than in the adapter
// that raises them.
var (
	// ReasonGatewayNotConfigured is a cell with no Gateway transport wired.
	// CategoryUnsupported: it is a fact about this cell's coverage, not a
	// transient failure, and retrying will not change it.
	ReasonGatewayNotConfigured = errs.Register("AI_GATEWAY_NOT_CONFIGURED", errs.CategoryUnsupported,
		"lane-l",
		"AI assistance is not available in this region yet. Deterministic processing is unaffected.")

	// ReasonGatewayUnavailable is a deadline, an open circuit or a transport
	// failure. CategoryUnavailable: an advisory call has no side effect, so a
	// retry is safe. ADR-0006 §2.4 requires the caller to render it as a
	// degraded product state rather than as an error.
	ReasonGatewayUnavailable = errs.Register("AI_GATEWAY_UNAVAILABLE", errs.CategoryUnavailable,
		"lane-l",
		"AI assistance is temporarily unavailable. Deterministic processing is unaffected and the request may be retried.")

	// ReasonGatewayReplyUnevidenced is a reply that cannot say which model,
	// provider, prompt and AI train produced it. Such output is discarded:
	// ADR-0006 §2.7 makes the boundary an evidenced transfer, and an output
	// whose provenance is unknown cannot be evidenced.
	ReasonGatewayReplyUnevidenced = errs.Register("AI_GATEWAY_REPLY_UNEVIDENCED", errs.CategoryInternal,
		"lane-l",
		"The AI Gateway returned a result without its provenance. The result was discarded.")

	// ReasonProposalNotAnOntologyNode is a classification proposal whose code
	// is not an `ontology:` identifier. The AI cannot name a node by any other
	// grammar, so the proposal is discarded rather than shown for review.
	ReasonProposalNotAnOntologyNode = errs.Register("AI_PROPOSAL_NOT_AN_ONTOLOGY_NODE", errs.CategoryInternal,
		"lane-l",
		"The AI Gateway proposed a code that is not an ontology node. The proposal was discarded.")
)
