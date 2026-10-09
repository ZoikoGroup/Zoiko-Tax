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
	"github.com/zoikogroup/zoikotax/backend/internal/domain/idempotency"
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
// compares bytes. It is internal and test use only — A2, not A3. The transport
// reaches it through POST /v1/quotes, POST /v1/transactions:commit,
// GET /v1/decisions/{id} and POST /v1/replay/{id} (W2 lane K).
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

	// idempotency guards Commit. Nil in a service built only to determine and
	// replay, whose Commit then refuses rather than committing unguarded.
	idempotency *Idempotency

	// fiscal holds the stores a commit's fiscal effects write to (fiscal.go).
	// Nil, or a bundle with no fiscal profile, records the decision alone.
	fiscal *FiscalStores
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

// WithIdempotency returns the service with Commit guarded by g.
func (s *DeterminationService) WithIdempotency(g *Idempotency) *DeterminationService {
	s.idempotency = g
	return s
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
	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return evidence.Decision{}, err
	}
	defer func() { _ = tx.Rollback(txCtx) }()

	d, err := s.determine(txCtx, sc, in)
	if err != nil {
		return evidence.Decision{}, err
	}
	if err := tx.Commit(txCtx); err != nil {
		return evidence.Decision{}, err
	}
	return d, nil
}

// determine records a decision inside the caller's transaction, against the
// active bundle. Determine and Commit differ only in what else commits with
// the row.
func (s *DeterminationService) determine(ctx context.Context, sc security.Context, in DetermineInput) (evidence.Decision, error) {
	b, err := s.active()
	if err != nil {
		return evidence.Decision{}, err
	}
	return s.determineWith(ctx, sc, in, b)
}

// determineWith records a decision against a given bundle: the active one for
// a commit, the original decision's for an adjustment.
func (s *DeterminationService) determineWith(ctx context.Context, sc security.Context, in DetermineInput, bundle *rule.Bundle) (evidence.Decision, error) {
	if strings.TrimSpace(in.BusinessKey) == "" {
		return evidence.Decision{}, errs.Invalid("businessKey", errs.ReasonMissingField, "A business key is required.")
	}
	b, env, envBytes, err := s.envelopeFor(bundle, in.EventTime, in.Input, in.Accumulators)
	if err != nil {
		return evidence.Decision{}, err
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

	if in.Supersedes != nil {
		if err := s.checkSupersedes(ctx, *in.Supersedes, in.BusinessKey); err != nil {
			return evidence.Decision{}, err
		}
	}
	if err := s.decisions.Append(ctx, rec); err != nil {
		return evidence.Decision{}, err
	}
	return d, nil
}

// active returns the active bundle, or the refusal a cell with none gives.
func (s *DeterminationService) active() (*rule.Bundle, error) {
	b := s.content.Current()
	if b == nil {
		return nil, errs.New(errs.CategoryUnavailable, errs.ReasonNoContentBundle,
			"The cell has no active content bundle. The request was not applied and may be retried.")
	}
	return b, nil
}

// envelope builds the envelope a determination at eventTime would record,
// against the active bundle, and encodes it.
func (s *DeterminationService) envelope(eventTime time.Time, input evidence.Input, accumulators map[string]fiscal.Money) (*rule.Bundle, evidence.Envelope, []byte, error) {
	b, err := s.active()
	if err != nil {
		if eventTime.IsZero() {
			return nil, evidence.Envelope{}, nil, errs.Invalid("eventTime", errs.ReasonMissingField, "An event time is required.")
		}
		return nil, evidence.Envelope{}, nil, err
	}
	return s.envelopeFor(b, eventTime, input, accumulators)
}

// envelopeFor builds and encodes the envelope against a given bundle.
func (s *DeterminationService) envelopeFor(b *rule.Bundle, eventTime time.Time, input evidence.Input, accumulators map[string]fiscal.Money) (*rule.Bundle, evidence.Envelope, []byte, error) {
	if eventTime.IsZero() {
		return nil, evidence.Envelope{}, nil, errs.Invalid("eventTime", errs.ReasonMissingField, "An event time is required.")
	}
	if err := matchReadSet(b.AccumulatorKeys(), accumulators); err != nil {
		return nil, evidence.Envelope{}, nil, err
	}

	// Both instants at canon/v1's precision before anything uses them, so the
	// evaluation sees exactly the instants the envelope will record and a
	// replay will read back (ADR-0011 P2).
	env := evidence.Envelope{
		DecisionTime: s.clock.Now().UTC().Truncate(time.Microsecond),
		EventTime:    eventTime.UTC().Truncate(time.Microsecond),
		BundleID:     b.ID(),
		BundleDigest: b.Digest(),
		IRVersion:    b.IRVersion(),
		CanonProfile: canonical.ProfileVersion,
		Trains:       s.trains,
		Input:        input,
		Accumulators: accumulators,
	}
	if err := env.Validate(); err != nil {
		return nil, evidence.Envelope{}, nil, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue,
			"The determination request cannot be recorded as evidence.")
	}
	envBytes, err := evidence.EncodeEnvelope(env)
	if err != nil {
		return nil, evidence.Envelope{}, nil, internal(err, "The determination could not be recorded.")
	}
	return b, env, envBytes, nil
}

// ---------------------------------------------------------------------------
// quote — ADR-0004 §2.7
// ---------------------------------------------------------------------------

// QuoteInput is one quote request: a determination with nothing recorded.
type QuoteInput struct {
	EventTime    time.Time
	Input        evidence.Input
	Accumulators map[string]fiscal.Money
}

// Quote is an estimate. It is evaluated by exactly the path a commit takes and
// then discarded: no evidence object, no decision row, no idempotency record.
type Quote struct {
	// QuotedAt is the decision time the evaluation ran at. A commit of the
	// same input later runs at a later one, and may differ.
	QuotedAt     time.Time
	BundleID     string
	BundleDigest string
	IRVersion    int
	Result       evidence.Result
}

// Quote evaluates the input against the active bundle without recording it.
//
// ADR-0004 §2.7: a quote is an estimate and only a commit is a decision. Its
// outcome is ADVISORY for the same structural reason every outcome is before
// A4 (evidence.Conclude), and a quote would be non-authoritative even after
// A4 — the transport says so on every response rather than leaving a caller
// to infer it.
func (s *DeterminationService) Quote(ctx context.Context, in QuoteInput) (Quote, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst); err != nil {
		return Quote{}, err
	}
	reads := in.Accumulators
	if active := s.content.Current(); active != nil {
		var err error
		if reads, err = s.quoteReadSet(ctx, active, in.EventTime, in.Accumulators); err != nil {
			return Quote{}, err
		}
	}
	b, env, envBytes, err := s.envelope(in.EventTime, in.Input, reads)
	if err != nil {
		return Quote{}, err
	}
	result, _, err := evaluate(b, env, canonical.SumBytes(envBytes))
	if err != nil {
		return Quote{}, err
	}
	return Quote{
		QuotedAt: env.DecisionTime, BundleID: env.BundleID, BundleDigest: env.BundleDigest,
		IRVersion: env.IRVersion, Result: result,
	}, nil
}

// ---------------------------------------------------------------------------
// commit — ADR-0013
// ---------------------------------------------------------------------------

// CommitEndpoint scopes commit's idempotency keys (ADR-0013 §2.2), so a key
// used here can never match one used for an adjust.
const CommitEndpoint = "POST /v1/transactions:commit"

// CommitRetention is how long a commit's idempotency record is kept.
//
// ADR-0013 §2.9 ties it to the statutory retention of the decision created,
// which a pack's legal profile will supply and nothing supplies yet. Ten years
// is at or beyond the fiscal record period the pack standard contemplates; it
// errs toward keeping a key live, because a key that expires while its
// decision is still retained is a key a late retry can use to create a second
// decision. §5.1 control 5 stays open until the profile replaces this.
const CommitRetention = 10 * 365 * 24 * time.Hour

// CommitInput is one commit request.
type CommitInput struct {
	IdempotencyKey string
	// Scope overrides the endpoint the key is scoped to. Empty is the
	// endpoint's own; a batch item sets its own scope, so a key a client
	// chose for :commit can never replay a batch item or be replayed by one.
	Scope         string
	Determination DetermineInput
	// Render produces the response a successful commit returns. It is the
	// transport's, and its bytes are what every retry of this key receives.
	Render func(evidence.Decision) ([]byte, error)
	// RenderFailure renders a deterministic failure the same way.
	RenderFailure func(error) Response
}

// Commit records a decision at most once per idempotency key.
func (s *DeterminationService) Commit(ctx context.Context, in CommitInput) (Settled, error) {
	sc, err := requireRoleOrSystem(ctx, security.RoleOperator)
	if err != nil {
		return Settled{}, err
	}
	if s.idempotency == nil {
		return Settled{}, errs.New(errs.CategoryInternal, errs.ReasonInternal,
			"The determination service was wired without an idempotency guard.")
	}
	digest, err := commitDigest(in.Determination)
	if err != nil {
		return Settled{}, err
	}
	return s.idempotency.Do(ctx, Call{
		Key:       idempotency.Key{TenantID: sc.Tenant(), Endpoint: scopeOr(in.Scope, CommitEndpoint), Value: in.IdempotencyKey},
		Digest:    digest,
		Retention: CommitRetention,
		Execute: func(ctx context.Context) (Response, error) {
			b, err := s.active()
			if err != nil {
				return Response{}, err
			}
			d, err := s.record(ctx, sc, in.Determination, b, nil)
			if err != nil {
				return Response{}, err
			}
			if err := s.emitDecision(ctx, d); err != nil {
				return Response{}, err
			}
			body, err := in.Render(d)
			if err != nil {
				return Response{}, internal(err, "The committed decision could not be rendered.")
			}
			return Response{Status: 201, Body: body, ResultRef: &d.ID}, nil
		},
		RenderFailure: in.RenderFailure,
	})
}

// ---------------------------------------------------------------------------
// adjust — ZTAX-DET-001 §10
// ---------------------------------------------------------------------------

// AdjustEndpoint scopes adjust's idempotency keys, apart from commit's.
const AdjustEndpoint = "POST /v1/transactions:adjust"

// Adjust records a CORRECTION of an earlier decision (ZTAX-DET-001 §10.4),
// at most once per idempotency key.
//
// It differs from a commit that names supersedes in one way, and it is the
// way DET-001 calls the single most consequential rule it has: the correction
// is evaluated against the content bundle that made the original decision,
// not the active one (ZTAX-DET-REQ-0030). A credit issued in 2029 against a
// 2027 invoice corrects tax charged at 2027 rates under 2027 rules; evaluating
// it under 2029 content would credit an amount that was never charged. The
// original bundle is the one the original's envelope names, so this needs no
// new machinery — only the discipline of using it. When that bundle is not
// loaded in this cell, the adjustment is refused as unavailable rather than
// quietly re-evaluated under whatever is active.
func (s *DeterminationService) Adjust(ctx context.Context, in CommitInput) (Settled, error) {
	sc, err := requireRoleOrSystem(ctx, security.RoleOperator)
	if err != nil {
		return Settled{}, err
	}
	if s.idempotency == nil {
		return Settled{}, errs.New(errs.CategoryInternal, errs.ReasonInternal,
			"The determination service was wired without an idempotency guard.")
	}
	if in.Determination.Supersedes == nil {
		return Settled{}, errs.Invalid("supersedes", errs.ReasonMissingField,
			"An adjustment names the decision it corrects.")
	}
	digest, err := commitDigest(in.Determination)
	if err != nil {
		return Settled{}, err
	}
	return s.idempotency.Do(ctx, Call{
		Key:       idempotency.Key{TenantID: sc.Tenant(), Endpoint: scopeOr(in.Scope, AdjustEndpoint), Value: in.IdempotencyKey},
		Digest:    digest,
		Retention: CommitRetention,
		Execute: func(ctx context.Context) (Response, error) {
			rec, b, err := s.originalBundle(ctx, *in.Determination.Supersedes)
			if err != nil {
				return Response{}, err
			}
			d, err := s.record(ctx, sc, in.Determination, b, &rec)
			if err != nil {
				return Response{}, err
			}
			if err := s.emitDecision(ctx, d); err != nil {
				return Response{}, err
			}
			body, err := in.Render(d)
			if err != nil {
				return Response{}, internal(err, "The adjustment could not be rendered.")
			}
			return Response{Status: 201, Body: body, ResultRef: &d.ID}, nil
		},
		RenderFailure: in.RenderFailure,
	})
}

// originalBundle returns the bundle that made a recorded decision.
func (s *DeterminationService) originalBundle(ctx context.Context, prior id.DecisionID) (evidence.Record, *rule.Bundle, error) {
	rec, err := s.decisions.ByID(ctx, prior)
	if err != nil {
		return evidence.Record{}, nil, err
	}
	b, ok := s.library.ByDigest(rec.BundleDigest)
	if !ok {
		return evidence.Record{}, nil, errs.New(errs.CategoryUnavailable, errs.ReasonNoContentBundle,
			"The content bundle that made the original decision is not loaded in this cell, and an adjustment is never re-evaluated under other content. The request was not applied and may be retried.")
	}
	return rec, b, nil
}

// commitDigest is the ADR-0013 §2.3 request digest: the canonical form of what
// was asked, so two retries that differ only in JSON member order, whitespace
// or an SDK's formatting match, and two that differ in any value — "45.00"
// against "45.0" included, because scale is semantic — do not.
func commitDigest(in DetermineInput) (canonical.Digest, error) {
	d, err := canonical.Sum(commitCanonical(in))
	if err != nil {
		return canonical.Digest{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue,
			"The request cannot be put in canonical form.")
	}
	return d, nil
}

// commitCanonical is a commit request in canonical form: what its digest
// covers, and what a queued batch item is stored as.
func commitCanonical(in DetermineInput) canonical.Value {
	supersedes := canonical.Absent()
	if in.Supersedes != nil {
		supersedes = canonical.String(in.Supersedes.String())
	}
	return canonical.Object(
		canonical.F("businessKey", canonical.String(in.BusinessKey)),
		canonical.F("supersedes", supersedes),
		canonical.F("eventTime", canonical.Time(in.EventTime.UTC().Truncate(time.Microsecond))),
		canonical.F("input", in.Input.Canonical()),
		canonical.F("accumulators", evidence.ReadSetCanonical(in.Accumulators)),
	)
}

func scopeOr(scope, endpoint string) string {
	if scope != "" {
		return scope
	}
	return endpoint
}

// ---------------------------------------------------------------------------
// read
// ---------------------------------------------------------------------------

// RecordedDecision is a decision as read back: its index row, and the result
// it names, read from evidence and verified against the row's digest.
type RecordedDecision struct {
	Record evidence.Record
	Result evidence.Result
}

// Decision reads one recorded decision.
func (s *DeterminationService) Decision(ctx context.Context, decisionID id.DecisionID) (RecordedDecision, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return RecordedDecision{}, err
	}
	rec, err := s.decisions.ByID(ctx, decisionID)
	if err != nil {
		return RecordedDecision{}, err
	}
	// Get verifies the bytes against the digest, so what is decoded here is
	// the result the row names and not whatever the store now holds.
	data, err := s.evidence.Get(ctx, rec.ResultDigest)
	if err != nil {
		return RecordedDecision{}, err
	}
	result, err := evidence.DecodeResult(data)
	if err != nil {
		return RecordedDecision{}, integrity(err, "The recorded result cannot be read as canonical evidence.")
	}
	if !result.EnvelopeDigest.Equal(rec.EnvelopeDigest) || result.Outcome != rec.Outcome {
		return RecordedDecision{}, errs.New(errs.CategoryInternal, errs.ReasonEvidenceIntegrity,
			"The decision's record disagrees with the result it names.")
	}
	return RecordedDecision{Record: rec, Result: result}, nil
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
