// Package gateway is the Go side of the Governed Model Gateway boundary
// (ADR-0006): the only code in the module that sends anything towards a
// model, and it sends it to the Gateway, never to a provider.
//
// # What the Client does, in order
//
//  1. Builds the governance context. The tenant comes from the request's
//     security context (ADR-0012 §2.7) and the region is the cell this process
//     runs in (ADR-0006 §2.8); neither is the caller's to supply.
//  2. Runs the policy gate (ai.Evaluate) before anything leaves the process.
//     ADR-0006 §2.5 makes the Python Gateway the enforcement point and that
//     does not change — this is defence in depth, and it can only refuse
//     earlier, never permit what the Gateway would refuse. What it buys is that
//     a refusal never leaves the process: an A5 request's input is never
//     transmitted, never logged by the Gateway and never on a network.
//  3. Consults the circuit breaker, then makes the call under a hard client-side
//     deadline (ADR-0006 §2.4). Synchronous calls are permitted only in
//     operator-initiated flows off the C0 path; the C0 path reaches the AI plane
//     through the outbox and never through this client.
//  4. Records the crossing — refused, failed or completed — as ADR-0006 §2.7's
//     evidenced transfer: use case, model, provider and prompt profile, region,
//     data classification, risk tier, authority outcome and AI train version.
//     Never the input and never the output.
//  5. Returns the result only as an internal/domain/ai advisory type. There is
//     no path from this package to a fiscal type (ADR-0006 §2.6).
//
// # The transports
//
// GRPC (grpc.go) is the production transport: gRPC over mTLS to the Python
// Gateway (ADR-0006 §2.1), carrying the JSON messages of
// contracts/schemas/ai/gateway-call and gateway-reply. intelligence/README.md
// ("The transport") records why the codec is JSON rather than protobuf.
//
// Unconfigured is the transport for a cell with no Gateway target. It refuses
// every call with AI_GATEWAY_NOT_CONFIGURED — a visible "this capability is not
// available in this cell" rather than a silent no-op or, worse, a stand-in
// that talks to a provider directly. gatewaytest.Fake is the in-memory
// Transport for tests, and is test-only.
//
// # What this package must never contain
//
// A provider SDK import or a provider hostname. Go workloads are denied egress
// to provider endpoints by network policy (ADR-0006 §2.1), the depguard rule
// no-direct-provider-call refuses the imports, and nodirect_test.go scans the
// whole module for both, so the policy is enforced three times and by review
// zero times.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// The adapter's own reason codes. They are about the transport, not about
// governance — a governance refusal is one of ai.RefusalCodes.
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
)

// Kind is which advisory record a call produces.
type Kind string

// The kinds, one per ADR-0006 §2.6 advisory type.
const (
	KindSuggestion             Kind = "SUGGESTION"
	KindExtraction             Kind = "EXTRACTION"
	KindClassificationProposal Kind = "CLASSIFICATION_PROPOSAL"
)

// Call is what crosses to the Gateway. It carries the full governance context
// so the Gateway makes its own decision on the same facts (ADR-0006 §2.5);
// the Go pre-check is never sent as a verdict the Gateway could defer to.
type Call struct {
	Kind       Kind
	Governance ai.GovernanceContext
	SubjectRef string
	// Input is the canonical-form input. The gate never read it.
	Input []byte
}

// Reply is what the Gateway returns. Only the routing names and the advisory
// content: the use case, region, risk tier and authority on the resulting
// record are the ones this process sent, never ones the reply asserts, so a
// reply cannot widen its own authority or relocate itself.
type Reply struct {
	// The routing the Gateway resolved (ADR-0006 §2.7). All four are required.
	ModelProfile    string
	ProviderProfile string
	PromptProfile   string
	AiTrainVersion  string

	// KindSuggestion.
	Text    string
	Payload string
	// KindExtraction.
	Fields map[string]string
	// KindClassificationProposal. Confidence is a decimal string or empty.
	ProposedCode string
	Confidence   string
}

// Transport carries a Call to the Gateway. An implementation returns an
// *errs.Error: a Gateway refusal as CategoryPolicy with the Gateway's reason
// code, and a transport failure as CategoryUnavailable. Anything else is
// treated as a defect.
type Transport interface {
	Invoke(ctx context.Context, call Call) (Reply, error)
}

// Unconfigured is the Transport for a cell with no Gateway. It refuses every
// call; see the package documentation for why it exists.
type Unconfigured struct{}

// Invoke refuses.
func (Unconfigured) Invoke(context.Context, Call) (Reply, error) {
	return Reply{}, errs.New(errs.CategoryUnsupported, ReasonGatewayNotConfigured,
		"AI assistance is not available in this region yet.")
}

// PolicySource supplies the current governance snapshot. It is read on every
// call so an operator's kill switch takes effect on the next request.
type PolicySource interface {
	Policy() ai.Policy
}

// StaticPolicy is a PolicySource that never changes.
type StaticPolicy struct{ P ai.Policy }

// Policy returns the snapshot.
func (s StaticPolicy) Policy() ai.Policy { return s.P }

// Config is the Client's wiring. Every field is required except Breaker.
type Config struct {
	Transport Transport
	Policy    PolicySource
	Recorder  TransferRecorder
	Clock     clock.Clock
	IDs       idgen.Generator
	// Region is the regional cell this process serves (ADR-0006 §2.8).
	Region string
	// Deadline is the hard client-side deadline on every call (§2.4). A
	// caller's own shorter deadline still wins, because context deadlines
	// only ever shorten.
	Deadline time.Duration
	// Breaker is optional; nil means a breaker with default settings.
	Breaker *Breaker
}

// Client is the governed Model Gateway client. It implements
// port.ModelGateway.
type Client struct {
	transport Transport
	policy    PolicySource
	recorder  TransferRecorder
	clock     clock.Clock
	ids       idgen.Generator
	region    string
	deadline  time.Duration
	breaker   *Breaker
}

// New validates the wiring and builds a Client.
func New(cfg Config) (*Client, error) {
	switch {
	case cfg.Transport == nil:
		return nil, fmt.Errorf("gateway: no transport (use Unconfigured for a cell without a Gateway)")
	case cfg.Policy == nil:
		return nil, fmt.Errorf("gateway: no policy source")
	case cfg.Recorder == nil:
		// No recorder means no evidenced transfer, and ADR-0006 §2.7 does not
		// have an "unless nobody wired one" clause.
		return nil, fmt.Errorf("gateway: no transfer recorder")
	case cfg.Clock == nil:
		return nil, fmt.Errorf("gateway: no clock")
	case cfg.IDs == nil:
		return nil, fmt.Errorf("gateway: no identifier generator")
	case cfg.Region == "":
		return nil, fmt.Errorf("gateway: no region")
	case cfg.Deadline <= 0:
		return nil, fmt.Errorf("gateway: deadline must be positive")
	}
	b := cfg.Breaker
	if b == nil {
		b = NewBreaker(DefaultBreakerThreshold, DefaultBreakerCooldown)
	}
	return &Client{
		transport: cfg.Transport,
		policy:    cfg.Policy,
		recorder:  cfg.Recorder,
		clock:     cfg.Clock,
		ids:       cfg.IDs,
		region:    cfg.Region,
		deadline:  cfg.Deadline,
		breaker:   b,
	}, nil
}

// Suggest asks the Gateway for an AiSuggestion.
func (c *Client) Suggest(ctx context.Context, inv ai.Invocation) (ai.AiSuggestion, error) {
	reply, prov, err := c.invoke(ctx, KindSuggestion, inv)
	if err != nil {
		return ai.AiSuggestion{}, err
	}
	u, err := c.ids.NewUUID()
	if err != nil {
		return ai.AiSuggestion{}, fmt.Errorf("gateway: generate id: %w", err)
	}
	sid, err := ai.NewAiSuggestionID(u.String())
	if err != nil {
		return ai.AiSuggestion{}, err
	}
	s, err := ai.NewAiSuggestion(sid, inv.SubjectRef, reply.Text, reply.Payload, prov, c.clock.Now())
	if err != nil {
		return ai.AiSuggestion{}, errs.Wrap(err, errs.CategoryInternal, ReasonGatewayReplyUnevidenced,
			"The AI Gateway returned a result that is not a valid suggestion.")
	}
	return s, nil
}

// Extract asks the Gateway for an AiExtraction.
func (c *Client) Extract(ctx context.Context, inv ai.Invocation) (ai.AiExtraction, error) {
	reply, prov, err := c.invoke(ctx, KindExtraction, inv)
	if err != nil {
		return ai.AiExtraction{}, err
	}
	u, err := c.ids.NewUUID()
	if err != nil {
		return ai.AiExtraction{}, fmt.Errorf("gateway: generate id: %w", err)
	}
	eid, err := ai.NewAiExtractionID(u.String())
	if err != nil {
		return ai.AiExtraction{}, err
	}
	e, err := ai.NewAiExtraction(eid, inv.SubjectRef, reply.Fields, prov)
	if err != nil {
		return ai.AiExtraction{}, errs.Wrap(err, errs.CategoryInternal, ReasonGatewayReplyUnevidenced,
			"The AI Gateway returned a result that is not a valid extraction.")
	}
	return e, nil
}

// ProposeClassification asks the Gateway for an AiClassificationProposal.
func (c *Client) ProposeClassification(ctx context.Context, inv ai.Invocation) (ai.AiClassificationProposal, error) {
	reply, prov, err := c.invoke(ctx, KindClassificationProposal, inv)
	if err != nil {
		return ai.AiClassificationProposal{}, err
	}
	u, err := c.ids.NewUUID()
	if err != nil {
		return ai.AiClassificationProposal{}, fmt.Errorf("gateway: generate id: %w", err)
	}
	pid, err := ai.NewAiClassificationProposalID(u.String())
	if err != nil {
		return ai.AiClassificationProposal{}, err
	}
	p, err := ai.NewAiClassificationProposal(pid, inv.SubjectRef, reply.ProposedCode, reply.Confidence, prov)
	if err != nil {
		return ai.AiClassificationProposal{}, errs.Wrap(err, errs.CategoryInternal, ReasonGatewayReplyUnevidenced,
			"The AI Gateway returned a result that is not a valid classification proposal.")
	}
	return p, nil
}

// invoke is the governed call. It returns the reply and the provenance to
// stamp on the advisory record, or an *errs.Error.
func (c *Client) invoke(ctx context.Context, kind Kind, inv ai.Invocation) (Reply, ai.Provenance, error) {
	// The zero tenant when there is no security context: the gate refuses it
	// as a malformed context, so a call with no tenant fails closed.
	var tenant id.TenantID
	if t, ok := security.MustTenant(ctx); ok {
		tenant = t
	}
	g := inv.Governance(tenant, c.region)
	t := Transfer{
		Tenant:      tenant,
		Kind:        kind,
		UseCase:     g.UseCase,
		Region:      g.Region,
		DataClass:   g.HighestDataClass(),
		DataClasses: g.DataClasses,
		RiskTier:    g.RiskTier,
		Authority:   g.AuthorityOutcome,
	}

	// 1. The policy gate. Nothing has left the process yet.
	if d := ai.Evaluate(c.policy.Policy(), g); !d.Allowed {
		return Reply{}, ai.Provenance{}, c.settle(ctx, t, OutcomeRefused, d.Reason, d.Err())
	}

	// 2. The circuit breaker.
	if !c.breaker.Allow(c.clock.Now()) {
		err := errs.New(errs.CategoryUnavailable, ReasonGatewayUnavailable,
			"AI assistance is temporarily unavailable.")
		return Reply{}, ai.Provenance{}, c.settle(ctx, t, OutcomeFailed, ReasonGatewayUnavailable, err)
	}

	// 3. The call, under the hard deadline.
	callCtx, cancel := context.WithTimeout(ctx, c.deadline)
	defer cancel()
	reply, err := c.transport.Invoke(callCtx, Call{
		Kind: kind, Governance: g, SubjectRef: inv.SubjectRef, Input: inv.Input,
	})
	if err != nil {
		err = classify(callCtx, err)
		if countsAgainstBreaker(err) {
			c.breaker.Failure(c.clock.Now())
		} else {
			// The Gateway answered — a refusal is a working Gateway.
			c.breaker.Success()
		}
		outcome := OutcomeFailed
		if errs.IsCategory(err, errs.CategoryPolicy) {
			outcome = OutcomeRefused
		}
		return Reply{}, ai.Provenance{}, c.settle(ctx, t, outcome, errs.ReasonOf(err), err)
	}
	c.breaker.Success()

	// 4. Provenance. The governance half is what this process sent; only the
	//    routing half comes from the reply.
	t.ModelProfile, t.ProviderProfile = reply.ModelProfile, reply.ProviderProfile
	t.PromptProfile, t.AiTrainVersion = reply.PromptProfile, reply.AiTrainVersion
	if reply.ModelProfile == "" || reply.ProviderProfile == "" || reply.PromptProfile == "" || reply.AiTrainVersion == "" {
		err := errs.New(errs.CategoryInternal, ReasonGatewayReplyUnevidenced,
			"The AI Gateway returned a result without its provenance. The result was discarded.")
		return Reply{}, ai.Provenance{}, c.settle(ctx, t, OutcomeDiscarded, ReasonGatewayReplyUnevidenced, err)
	}
	prov := ai.Provenance{
		UseCase:          g.UseCase,
		ModelProfile:     reply.ModelProfile,
		ProviderProfile:  reply.ProviderProfile,
		PromptProfile:    reply.PromptProfile,
		Region:           g.Region,
		DataClass:        string(g.HighestDataClass()),
		RiskTier:         g.RiskTier,
		AuthorityOutcome: g.AuthorityOutcome,
		AiTrainVersion:   reply.AiTrainVersion,
	}

	// 5. Evidence. An output whose crossing could not be recorded is not
	//    returned: ADR-0006 §2.7 has no "unless the log was down" clause.
	if err := c.record(ctx, t, OutcomeCompleted, ""); err != nil {
		return Reply{}, ai.Provenance{}, errs.Wrap(err, errs.CategoryUnavailable, ReasonGatewayUnavailable,
			"AI assistance is temporarily unavailable.")
	}
	return reply, prov, nil
}

// settle records an unsuccessful crossing and returns the error the caller
// sees. A recorder failure is joined rather than substituted, so the caller
// still sees why the call itself did not complete.
func (c *Client) settle(ctx context.Context, t Transfer, outcome TransferOutcome, reason errs.ReasonCode, cause error) error {
	if err := c.record(ctx, t, outcome, reason); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (c *Client) record(ctx context.Context, t Transfer, outcome TransferOutcome, reason errs.ReasonCode) error {
	t.Outcome, t.Reason, t.At = outcome, reason, c.clock.Now()
	if err := c.recorder.RecordTransfer(ctx, t); err != nil {
		return fmt.Errorf("gateway: record transfer: %w", err)
	}
	return nil
}

// classify maps a transport error onto the estate's taxonomy. An *errs.Error
// passes through; a deadline or cancellation is Unavailable; anything else is
// a transport failure the implementation did not classify, which is treated as
// Unavailable as well — an advisory call has no side effect to be uncertain
// about, so retry is safe.
func classify(ctx context.Context, err error) error {
	var e *errs.Error
	if errors.As(err, &e) {
		return err
	}
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return errs.Wrap(err, errs.CategoryUnavailable, ReasonGatewayUnavailable,
			"AI assistance did not respond in time.")
	}
	return errs.Wrap(err, errs.CategoryUnavailable, ReasonGatewayUnavailable,
		"AI assistance is temporarily unavailable.")
}

// countsAgainstBreaker reports whether an error is evidence the Gateway is
// unhealthy. A policy refusal, a validation failure or a not-configured cell
// is the Gateway (or this process) working as designed.
func countsAgainstBreaker(err error) bool {
	switch errs.CategoryOf(err) {
	case errs.CategoryUnavailable, errs.CategoryInternal:
		return true
	}
	return false
}
