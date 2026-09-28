package app

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// DeterminationService records determinations and replays them.
//
// This is the W1 exit gate's "one decision type replays exactly": a decision
// from the active content bundle is recorded as two evidence objects and an
// index row, and Replay rebuilds its result from the envelope alone and
// compares bytes. It is internal and test use only — A2, not A3 — and it is not
// on the transport surface: the quote and commit endpoints that will call it
// are W2 lane K.
//
// Every figure it records is ADVISORY (evidence.Conclude). That is structural
// rather than configured: there is no path through this service that can
// produce an AUTHORITATIVE outcome.
type DeterminationService struct {
	content   *rule.Holder
	library   *rule.Library
	decisions port.DecisionRepository
	evidence  port.EvidenceStore
	tx        port.TxManager
	clock     clock.Clock
	ids       idgen.Generator
	trains    evidence.Trains
}

// NewDeterminationService wires the service. trains are the seven release-train
// versions this process runs with; they go into every envelope.
func NewDeterminationService(
	content *rule.Holder,
	library *rule.Library,
	decisions port.DecisionRepository,
	store port.EvidenceStore,
	tx port.TxManager,
	clk clock.Clock,
	ids idgen.Generator,
	trains evidence.Trains,
) *DeterminationService {
	return &DeterminationService{
		content: content, library: library, decisions: decisions, evidence: store,
		tx: tx, clock: clk, ids: ids, trains: trains,
	}
}

// DetermineInput is one determination request.
type DetermineInput struct {
	// BusinessKey is the caller's stable reference for the thing being
	// determined — the line, the transaction. It is the ADR-0003 identity
	// across versions.
	BusinessKey string
	// Supersedes names the decision this one corrects, if any. It must be the
	// current version of the same business key (ZTAX-DET-REQ-0032).
	Supersedes *id.DecisionID
	// EventTime is when the taxable thing happened.
	EventTime time.Time
	Input     evidence.Input
	// Accumulators is the read set: a value for exactly the accumulators the
	// active bundle reads, no more and no fewer.
	//
	// It is supplied by the caller because the accumulator store of ADR-0004
	// is W2 lane I and does not exist yet. What matters for replay is already
	// true either way: the values are read before evaluation, passed in, and
	// recorded in the envelope (ZTAX-DET-REQ-0002, -0034). When the store
	// lands, it replaces the caller as the source and nothing downstream of
	// this field changes.
	Accumulators map[string]fiscal.Money
}

// Determine evaluates the input against the active bundle and records the
// decision.
func (s *DeterminationService) Determine(ctx context.Context, in DetermineInput) (evidence.Decision, error) {
	sc, err := requireRoleOrSystem(ctx, security.RoleOperator)
	if err != nil {
		return evidence.Decision{}, err
	}
	if strings.TrimSpace(in.BusinessKey) == "" {
		return evidence.Decision{}, errs.Invalid("businessKey", errs.ReasonMissingField, "A business key is required.")
	}
	if in.EventTime.IsZero() {
		return evidence.Decision{}, errs.Invalid("eventTime", errs.ReasonMissingField, "An event time is required.")
	}
	b := s.content.Current()
	if b == nil {
		return evidence.Decision{}, errs.New(errs.CategoryUnavailable, errs.ReasonNoContentBundle,
			"The cell has no active content bundle. The request was not applied and may be retried.")
	}
	if err := matchReadSet(b.AccumulatorKeys(), in.Accumulators); err != nil {
		return evidence.Decision{}, err
	}

	// Both instants at canon/v1's precision before anything uses them, so the
	// evaluation sees exactly the instants the envelope will record and a
	// replay will read back (ADR-0011 P2).
	env := evidence.Envelope{
		DecisionTime: s.clock.Now().UTC().Truncate(time.Microsecond),
		EventTime:    in.EventTime.UTC().Truncate(time.Microsecond),
		BundleID:     b.ID(),
		BundleDigest: b.Digest(),
		IRVersion:    b.IRVersion(),
		CanonProfile: canonical.ProfileVersion,
		Trains:       s.trains,
		Input:        in.Input,
		Accumulators: in.Accumulators,
	}
	if err := env.Validate(); err != nil {
		return evidence.Decision{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue,
			"The determination request cannot be recorded as evidence.")
	}
	envBytes, err := evidence.EncodeEnvelope(env)
	if err != nil {
		return evidence.Decision{}, internal(err, "The determination could not be recorded.")
	}
	envDigest := canonical.SumBytes(envBytes)

	result, resultBytes, err := evaluate(b, env, envDigest)
	if err != nil {
		return evidence.Decision{}, err
	}

	decisionID, err := idgen.DecisionID(s.ids)
	if err != nil {
		return evidence.Decision{}, internal(err, "The determination could not be recorded.")
	}
	d := evidence.Decision{
		ID:             decisionID,
		TenantID:       sc.Tenant(),
		BusinessKey:    in.BusinessKey,
		Supersedes:     in.Supersedes,
		Envelope:       env,
		EnvelopeDigest: envDigest,
		Result:         result,
		ResultDigest:   canonical.SumBytes(resultBytes),
	}
	rec, err := evidence.NewRecord(d)
	if err != nil {
		return evidence.Decision{}, internal(err, "The determination could not be recorded.")
	}

	// The evidence objects are written before the row that names them. They
	// are content-addressed, so a crash between the two leaves an object
	// nothing names — harmless, and found by nothing — whereas the other order
	// would leave a committed decision naming evidence that does not exist.
	for _, obj := range []struct {
		data []byte
		want canonical.Digest
	}{{envBytes, d.EnvelopeDigest}, {resultBytes, d.ResultDigest}} {
		got, err := s.evidence.Put(ctx, obj.data)
		if err != nil {
			return evidence.Decision{}, err
		}
		if !got.Equal(obj.want) {
			return evidence.Decision{}, errs.New(errs.CategoryInternal, errs.ReasonEvidenceIntegrity,
				fmt.Sprintf("The evidence store keyed an object as %s; its digest is %s.", got, obj.want))
		}
	}

	tx, ctx, err := s.tx.Begin(ctx)
	if err != nil {
		return evidence.Decision{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if in.Supersedes != nil {
		if err := s.checkSupersedes(ctx, *in.Supersedes, in.BusinessKey); err != nil {
			return evidence.Decision{}, err
		}
	}
	if err := s.decisions.Append(ctx, rec); err != nil {
		return evidence.Decision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return evidence.Decision{}, err
	}
	return d, nil
}

// Replay rebuilds a recorded decision's result from its envelope and compares
// it, byte for byte, with the result that was recorded.
//
// Nothing the replay reads comes from the row except the two digests, and both
// are checked against the objects they name. The bundle is the one the
// envelope names — never the active one — so a decision replays under the
// content that made it.
func (s *DeterminationService) Replay(ctx context.Context, decisionID id.DecisionID) (evidence.ReplayReport, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return evidence.ReplayReport{}, err
	}
	rec, err := s.decisions.ByID(ctx, decisionID)
	if err != nil {
		return evidence.ReplayReport{}, err
	}
	report := evidence.ReplayReport{
		DecisionID:     rec.DecisionID,
		EnvelopeDigest: rec.EnvelopeDigest,
		RecordedResult: rec.ResultDigest,
	}

	envBytes, err := s.evidence.Get(ctx, rec.EnvelopeDigest)
	if err != nil {
		return evidence.ReplayReport{}, err
	}
	env, err := evidence.DecodeEnvelope(envBytes)
	if err != nil {
		return evidence.ReplayReport{}, integrity(err, "The recorded envelope cannot be read as canonical evidence.")
	}
	// The row's own claims about the envelope must agree with the envelope.
	// A disagreement means the index and the evidence have come apart, which
	// is an integrity incident whatever the replay would have said.
	inputDigest, err := env.InputDigest()
	if err != nil {
		return evidence.ReplayReport{}, integrity(err, "The recorded input cannot be digested.")
	}
	if !inputDigest.Equal(rec.InputDigest) || env.BundleDigest != rec.BundleDigest || !env.DecisionTime.Equal(rec.RecordedAt) {
		return evidence.ReplayReport{}, errs.New(errs.CategoryInternal, errs.ReasonEvidenceIntegrity,
			"The decision's record disagrees with the envelope it names.")
	}

	b, ok := s.library.ByDigest(env.BundleDigest)
	if !ok {
		report.Verdict = evidence.ReplayBundleUnavailable
		return report, nil
	}

	_, replayed, err := evaluate(b, env, rec.EnvelopeDigest)
	if err != nil {
		// The original evaluation succeeded on this envelope, or there would
		// be no record. Failing now is the strongest form of divergence.
		report.Verdict = evidence.ReplayDiverged
		report.Divergence = "evaluation failed on replay: " + err.Error()
		return report, nil
	}
	report.ReplayedResult = canonical.SumBytes(replayed)

	recorded, err := s.evidence.Get(ctx, rec.ResultDigest)
	if err != nil {
		return evidence.ReplayReport{}, err
	}
	if match, where := evidence.CompareResults(recorded, replayed); match {
		report.Verdict = evidence.ReplayMatch
	} else {
		report.Verdict = evidence.ReplayDiverged
		report.Divergence = where
	}
	return report, nil
}

// evaluate is the one evaluation path, shared by Determine and Replay so that
// a replay exercises exactly the code production ran (ADR-0003 §3.3) rather
// than a parallel implementation that could agree with itself.
func evaluate(b *rule.Bundle, env evidence.Envelope, envDigest canonical.Digest) (evidence.Result, []byte, error) {
	res, err := rule.Evaluate(b, env.Frame())
	if err != nil {
		if errs.CategoryOf(err) != errs.CategoryInternal {
			return evidence.Result{}, nil, err
		}
		// The evaluator reports a frame that does not carry what content reads
		// — an absent input, most often. That is the request's shape, not a
		// defect in the cell.
		return evidence.Result{}, nil, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue,
			"The transaction does not carry what the active content requires: "+err.Error())
	}
	outcome, reason := evidence.Conclude(res)
	result := evidence.Result{
		EnvelopeDigest: envDigest,
		Outcome:        outcome,
		Reason:         reason,
		Emitted:        res.Emitted,
		Trace:          res.Trace,
	}
	encoded, err := result.Encode()
	if err != nil {
		return evidence.Result{}, nil, internal(err, "The determination result could not be encoded.")
	}
	return result, encoded, nil
}

// checkSupersedes refuses a correction that does not correct the current
// version of the same business key.
func (s *DeterminationService) checkSupersedes(ctx context.Context, prior id.DecisionID, businessKey string) error {
	old, err := s.decisions.ByID(ctx, prior)
	if err != nil {
		return err
	}
	if old.BusinessKey != businessKey {
		return errs.Invalid("supersedes", errs.ReasonInvalidValue,
			"A correction supersedes a decision for the same business key.")
	}
	history, err := s.decisions.History(ctx, businessKey)
	if err != nil {
		return err
	}
	for _, h := range history {
		if h.Supersedes != nil && *h.Supersedes == prior {
			// The unique index would refuse this too; saying so here gives the
			// caller a reason code rather than a constraint name.
			return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				"That decision has already been superseded. Correct the current version instead.")
		}
	}
	return nil
}

// matchReadSet refuses a read set that is not exactly the bundle's. A missing
// value is not zero (the evaluator refuses it anyway), and an extra value is
// something recorded in the envelope that the decision did not depend on —
// which would make the envelope claim a dependency that is not there.
func matchReadSet(want []string, got map[string]fiscal.Money) error {
	have := make([]string, 0, len(got))
	for k := range got {
		have = append(have, k)
	}
	sort.Strings(have)
	if slices.Equal(want, have) {
		return nil
	}
	return errs.Invalid("accumulators", errs.ReasonInvalidValue,
		fmt.Sprintf("The active content reads accumulators [%s]; the request supplied [%s].",
			strings.Join(want, ", "), strings.Join(have, ", ")))
}

// requireRoleOrSystem admits an authenticated subject holding one of roles, or
// in-process system work scoped to a tenant (security.System) — a replay
// certification sweep, a sealing job. An unauthenticated request carries
// neither and is refused.
func requireRoleOrSystem(ctx context.Context, roles ...security.Role) (security.Context, error) {
	sc, ok := security.From(ctx)
	if !ok || sc.Tenant().IsZero() {
		return security.Context{}, errs.New(errs.CategoryPolicy, errs.ReasonUnauthenticated,
			"The request carried no valid session. Sign in and retry.")
	}
	if sc.Subject().IsZero() {
		// System work. It is constructed inside the process and never from a
		// credential, so reaching here without a subject means a caller in
		// this binary chose to act for the tenant.
		return sc, nil
	}
	if !sc.HasAny(roles...) {
		return security.Context{}, errs.New(errs.CategoryPolicy, errs.ReasonForbidden,
			"The authenticated subject does not hold a role permitting this action.")
	}
	return sc, nil
}

func internal(err error, detail string) error {
	return errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, detail)
}

func integrity(err error, detail string) error {
	return errs.Wrap(err, errs.CategoryInternal, errs.ReasonEvidenceIntegrity, detail)
}
