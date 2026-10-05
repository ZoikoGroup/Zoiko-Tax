package app

import (
	"context"
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/accumulator"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
)

// fiscalActive reports whether a commit under b has fiscal effects to apply.
func (s *DeterminationService) fiscalActive(b *rule.Bundle) bool {
	return b != nil && s.fiscal != nil && s.fiscal.complete() && b.Fiscal() != nil
}

// record records a commit — or, with prior set, an adjustment of prior —
// under bundle b, with the bundle's fiscal effects in the caller's
// transaction.
func (s *DeterminationService) record(ctx context.Context, sc security.Context, in DetermineInput, b *rule.Bundle, prior *evidence.Record) (evidence.Decision, error) {
	if !s.fiscalActive(b) {
		return s.determineWith(ctx, sc, in, b)
	}
	p := b.Fiscal()
	if in.EventTime.IsZero() {
		return evidence.Decision{}, errs.Invalid("eventTime", errs.ReasonMissingField, "An event time is required.")
	}
	bsNew, err := bindAccumulators(p, in.EventTime)
	if err != nil {
		return evidence.Decision{}, err
	}
	for _, bn := range bsNew {
		if _, supplied := in.Accumulators[bn.binding.Read]; supplied {
			// ZTAX-DET-REQ-0002: the values are read before evaluation, by
			// the cell. A caller-supplied total for a bound accumulator would
			// let a commit decide against a number the store never held.
			return evidence.Decision{}, errs.Invalid("accumulators", errs.ReasonInvalidValue,
				fmt.Sprintf("The active content reads %s from the cell; a commit does not supply it.", bn.binding.Read))
		}
	}

	var (
		bsOld      []bound
		origEnv    evidence.Envelope
		origResult evidence.Result
	)
	if prior != nil {
		if origEnv, origResult, err = s.recordedEvidence(ctx, *prior); err != nil {
			return evidence.Decision{}, err
		}
		if bsOld, err = bindAccumulators(p, origEnv.EventTime); err != nil {
			return evidence.Decision{}, err
		}
	}
	held, err := s.lock(ctx, bsNew, bsOld)
	if err != nil {
		return evidence.Decision{}, err
	}

	// The read set: the store's totals for a commit; for a correction, the
	// totals the original saw, because the correction re-decides the same
	// transaction at the same point in its period.
	reads := readSet(bsNew, held)
	if prior != nil {
		reads = origEnv.Accumulators
	}
	merged := make(map[string]fiscal.Money, len(in.Accumulators)+len(reads))
	for k, v := range in.Accumulators {
		merged[k] = v
	}
	for k, v := range reads {
		merged[k] = v
	}
	in.Accumulators = merged

	d, err := s.determineWith(ctx, sc, in, b)
	if err != nil {
		return evidence.Decision{}, err
	}

	// A correction withdraws what the corrected decision contributed — its
	// full amount, from its own period — and adds its own. Merged per key, so
	// the correction contributes to a key once.
	var cs []contribution
	if prior != nil {
		for _, ob := range bsOld {
			x, ok := contributionAmount(ob.binding, origEnv.Input, origResult)
			if !ok {
				continue
			}
			contributed, err := s.contributed(ctx, ob.ref.Key, prior.DecisionID)
			if err != nil {
				return evidence.Decision{}, err
			}
			if contributed {
				cs = append(cs, contribution{b: ob, amount: x.Neg()})
			}
		}
	}
	for _, nb := range bsNew {
		if y, ok := contributionAmount(nb.binding, d.Envelope.Input, d.Result); ok {
			cs = append(cs, contribution{b: nb, amount: y})
		}
	}
	if err := s.contribute(ctx, sc.Tenant(), d, cs, held); err != nil {
		return evidence.Decision{}, err
	}
	if prior != nil {
		if err := s.reversePosting(ctx, prior.DecisionID, d.Envelope.DecisionTime); err != nil {
			return evidence.Decision{}, err
		}
	}
	if err := s.post(ctx, sc.Tenant(), b, d); err != nil {
		return evidence.Decision{}, err
	}
	return d, nil
}

// contributed reports whether a decision contributed to a key.
func (s *DeterminationService) contributed(ctx context.Context, key accumulator.Key, decisionID id.DecisionID) (bool, error) {
	_, err := s.fiscal.Accumulators.Contribution(ctx, key, decisionID)
	if errs.IsCategory(err, errs.CategoryNotFound) {
		return false, nil
	}
	return err == nil, err
}

// recordedEvidence reads a recorded decision's envelope and result, each
// verified against its digest by the evidence store.
func (s *DeterminationService) recordedEvidence(ctx context.Context, rec evidence.Record) (evidence.Envelope, evidence.Result, error) {
	envBytes, err := s.evidence.Get(ctx, rec.EnvelopeDigest)
	if err != nil {
		return evidence.Envelope{}, evidence.Result{}, err
	}
	env, err := evidence.DecodeEnvelope(envBytes)
	if err != nil {
		return evidence.Envelope{}, evidence.Result{}, integrity(err, "The recorded envelope cannot be read as canonical evidence.")
	}
	resBytes, err := s.evidence.Get(ctx, rec.ResultDigest)
	if err != nil {
		return evidence.Envelope{}, evidence.Result{}, err
	}
	res, err := evidence.DecodeResult(resBytes)
	if err != nil {
		return evidence.Envelope{}, evidence.Result{}, integrity(err, "The recorded result cannot be read as canonical evidence.")
	}
	return env, res, nil
}

// quoteReadSet fills a quote's read set from the store, without locks, for the
// bound accumulators the caller did not supply. A quote may supply them — it
// is an estimate, and a what-if total is a legitimate question to ask it.
func (s *DeterminationService) quoteReadSet(ctx context.Context, b *rule.Bundle, eventTime time.Time, supplied map[string]fiscal.Money) (map[string]fiscal.Money, error) {
	if !s.fiscalActive(b) || eventTime.IsZero() {
		return supplied, nil
	}
	bs, err := bindAccumulators(b.Fiscal(), eventTime)
	if err != nil {
		return nil, err
	}
	var missing []bound
	for _, bn := range bs {
		if _, ok := supplied[bn.binding.Read]; !ok {
			missing = append(missing, bn)
		}
	}
	if len(missing) == 0 {
		return supplied, nil
	}
	observed, err := s.observedReadSet(ctx, missing)
	if err != nil {
		return nil, err
	}
	out := make(map[string]fiscal.Money, len(supplied)+len(observed))
	for k, v := range supplied {
		out[k] = v
	}
	for k, v := range observed {
		out[k] = v
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// The subledger, read
// ---------------------------------------------------------------------------

// DecisionJournals returns the journals a decision's commit posted and, for a
// superseded decision, the reversals its correction posted for it.
func (s *DeterminationService) DecisionJournals(ctx context.Context, decisionID id.DecisionID) ([]subledger.Journal, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return nil, err
	}
	if s.fiscal == nil || !s.fiscal.complete() {
		return nil, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable, "The subledger is not wired in this cell.")
	}
	if _, err := s.decisions.ByID(ctx, decisionID); err != nil {
		return nil, err
	}
	posted, err := s.fiscal.Journals.BySource(ctx, SourceDecisionCommitted, decisionID.String())
	if err != nil {
		return nil, err
	}
	reversed, err := s.fiscal.Journals.BySource(ctx, SourceDecisionSuperseded, decisionID.String())
	if err != nil {
		return nil, err
	}
	return append(posted, reversed...), nil
}

// Balances returns the control balances of the tenant's default legal entity.
func (s *DeterminationService) Balances(ctx context.Context) (id.LegalEntityID, map[subledger.BalanceKey]subledger.Balance, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return id.LegalEntityID{}, nil, err
	}
	if s.fiscal == nil || !s.fiscal.complete() {
		return id.LegalEntityID{}, nil, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable, "The subledger is not wired in this cell.")
	}
	le, err := s.fiscal.LegalEntities.Default(ctx)
	if err != nil {
		return id.LegalEntityID{}, nil, err
	}
	bal, err := s.fiscal.Journals.Balances(ctx, le.ID)
	return le.ID, bal, err
}
