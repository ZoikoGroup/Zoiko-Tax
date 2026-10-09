package app

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/accumulator"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/outbox"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// What a commit does beyond recording a decision, when the bundle's fiscal
// profile asks for it (content.FiscalProfile):
//
//  1. Before evaluation, the accumulators the profile binds are read from the
//     store under their row locks, in canonical order (ADR-0004 §2.2-§2.3),
//     and become the envelope's read set. The cell is the source of these
//     values; a request that supplies them is refused (ZTAX-DET-REQ-0002).
//  2. After the decision is recorded, each binding's contribution is applied:
//     the event appended, idempotent on (key, decision) (ZTAX-DET-REQ-0028),
//     the snapshot saved, and every threshold crossed recorded with an outbox
//     event in the same transaction (ZTAX-DET-REQ-0027).
//  3. The decision is posted to the Tax Control Subledger: one balanced
//     journal per currency for the tenant's default legal entity, once per
//     source event (ZTAX-FIN-REQ-0087).
//
// All of it runs in the commit's transaction, with the idempotency record, so
// a commit either records, contributes and posts, or does none of the three.

// FiscalStores are the stores a commit's fiscal effects write to.
type FiscalStores struct {
	Accumulators  port.AccumulatorRepository
	Journals      port.JournalRepository
	LegalEntities port.LegalEntityRepository
	Outbox        port.OutboxWriter
	Obligations   port.ObligationRepository
	// Periods guards every posting against its legal period's state
	// (period.go). Optional so a store wired without period close posts
	// as though every period were open; a cell always wires it.
	Periods port.PeriodRepository
}

func (f FiscalStores) complete() bool {
	return f.Accumulators != nil && f.Journals != nil && f.LegalEntities != nil && f.Outbox != nil &&
		f.Obligations != nil
}

// WithFiscal returns the service with fiscal effects wired.
func (s *DeterminationService) WithFiscal(f FiscalStores) *DeterminationService {
	s.fiscal = &f
	return s
}

// The source event kinds a commit posts under.
const (
	SourceDecisionCommitted  = "DECISION_COMMITTED"
	SourceDecisionSuperseded = "DECISION_SUPERSEDED"
)

// The threshold-crossed event (contracts/schemas/events/threshold-crossed).
const (
	EventThresholdCrossed     = outbox.TypeNamespace + ".accumulator.threshold-crossed"
	SchemaThresholdCrossedRef = "ztax:events/threshold-crossed/1.0.0"
)

// bound is one accumulator binding resolved for an event time.
type bound struct {
	binding    content.AccumulatorBinding
	ref        accumulator.Ref
	thresholds []accumulator.Threshold
}

// bindAccumulators resolves a profile's bindings for an event time: the
// stored key is the read key plus the legal period the event falls in, in the
// binding's own civil calendar.
func bindAccumulators(p *content.FiscalProfile, eventTime time.Time) ([]bound, error) {
	if p == nil {
		return nil, nil
	}
	out := make([]bound, 0, len(p.Accumulators))
	for _, a := range p.Accumulators {
		loc, err := time.LoadLocation(a.Timezone)
		if err != nil {
			return nil, internal(err, fmt.Sprintf("The content names legal timezone %q, which this cell cannot load.", a.Timezone))
		}
		rule := obligation.PeriodRule{Kind: obligation.PeriodKind(a.Period), Timezone: loc.String(), YearStartMonth: time.Month(a.YearStartMonth)}
		span, err := rule.PeriodFor(eventTime, loc)
		if err != nil {
			return nil, internal(err, "The content's accumulator period cannot place this event.")
		}
		key, err := accumulator.ParseKey(a.Read + "@" + span.Start.In(loc).Format(time.DateOnly))
		if err != nil {
			return nil, internal(err, "The content's accumulator key is not a valid key.")
		}
		b := bound{binding: a, ref: accumulator.Ref{Key: key, Currency: fiscal.Currency(a.Currency)}}
		for _, t := range a.Thresholds {
			limit, err := fiscal.ParseMoney(t.Limit, fiscal.Currency(a.Currency))
			if err != nil {
				return nil, internal(err, "The content's threshold limit is not money.")
			}
			cmp := accumulator.AtOrAbove
			if t.Comparison == "GT" {
				cmp = accumulator.Above
			}
			b.thresholds = append(b.thresholds, accumulator.Threshold{ID: accumulator.ThresholdID(t.ID), Limit: limit, Comparison: cmp})
		}
		out = append(out, b)
	}
	return out, nil
}

// lock locks every distinct accumulator the bindings name, in canonical
// order, and returns the snapshots by key.
func (s *DeterminationService) lock(ctx context.Context, sets ...[]bound) (map[accumulator.Key]accumulator.Snapshot, error) {
	seen := map[accumulator.Key]bool{}
	var refs []accumulator.Ref
	for _, bs := range sets {
		for _, b := range bs {
			if !seen[b.ref.Key] {
				seen[b.ref.Key] = true
				refs = append(refs, b.ref)
			}
		}
	}
	if len(refs) == 0 {
		return map[accumulator.Key]accumulator.Snapshot{}, nil
	}
	snaps, err := s.fiscal.Accumulators.LockAll(ctx, refs)
	if err != nil {
		return nil, err
	}
	held := make(map[accumulator.Key]accumulator.Snapshot, len(snaps))
	for _, sn := range snaps {
		held[sn.Key] = sn
	}
	return held, nil
}

// readSet is the totals the bindings read, keyed as the rules read them.
func readSet(bs []bound, held map[accumulator.Key]accumulator.Snapshot) map[string]fiscal.Money {
	out := make(map[string]fiscal.Money, len(bs))
	for _, b := range bs {
		out[b.binding.Read] = held[b.ref.Key].Total
	}
	return out
}

// observedReadSet reads the bound accumulators without locks, for a quote.
func (s *DeterminationService) observedReadSet(ctx context.Context, bs []bound) (map[string]fiscal.Money, error) {
	reads := map[string]fiscal.Money{}
	for _, b := range bs {
		o, err := s.fiscal.Accumulators.ReadUnlocked(ctx, b.ref)
		if err != nil {
			return nil, err
		}
		reads[b.binding.Read] = o.Total
	}
	return reads, nil
}

// contributionAmount is what a decision adds to one binding, or false when
// the source has nothing to add.
func contributionAmount(b content.AccumulatorBinding, input evidence.Input, result evidence.Result) (fiscal.Money, bool) {
	if b.Contributes.Input != "" {
		m, ok := input.Money[b.Contributes.Input]
		return m, ok
	}
	v, ok := result.Emitted[b.Contributes.Emitted]
	if !ok || v.Type != rule.TypeMoney {
		return fiscal.Money{}, false
	}
	return v.Money, true
}

// contribution is one decision's net effect on one stored accumulator.
type contribution struct {
	b      bound
	amount fiscal.Money
}

// mergeContributions sums contributions to the same stored key, so a decision
// contributes to a key at most once (ZTAX-DET-REQ-0028) even when a correction
// both withdraws from and adds to one period. Order is the key's, so writes
// are deterministic.
func mergeContributions(cs []contribution) ([]contribution, error) {
	byKey := map[accumulator.Key]*contribution{}
	var keys []accumulator.Key
	for _, c := range cs {
		if prior, ok := byKey[c.b.ref.Key]; ok {
			sum, err := prior.amount.Add(c.amount)
			if err != nil {
				return nil, internal(err, "Contributions to one accumulator could not be combined.")
			}
			prior.amount = sum
			continue
		}
		cc := c
		byKey[c.b.ref.Key] = &cc
		keys = append(keys, c.b.ref.Key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]contribution, len(keys))
	for i, k := range keys {
		out[i] = *byKey[k]
	}
	return out, nil
}

// contribute applies a decision's contributions to the held snapshots and
// writes the events, snapshots, crossings and crossing events.
func (s *DeterminationService) contribute(ctx context.Context, tenant id.TenantID, d evidence.Decision,
	cs []contribution, held map[accumulator.Key]accumulator.Snapshot) error {
	merged, err := mergeContributions(cs)
	if err != nil {
		return err
	}
	for _, c := range merged {
		if c.amount.Currency() != c.b.ref.Currency {
			return errs.Invalid("accumulators", errs.ReasonCurrencyMismatch,
				fmt.Sprintf("Accumulator %s is in %s and this decision contributes %s.", c.b.binding.Read, c.b.ref.Currency, c.amount.Currency()))
		}
		in := accumulator.Contribution{
			Key: c.b.ref.Key, SourceDecisionID: d.ID, Amount: c.amount,
			EventTime: d.Envelope.EventTime, RecordedAt: d.Envelope.DecisionTime,
		}
		next, crossings, err := accumulator.Apply(held[c.b.ref.Key], in, c.b.thresholds)
		if err != nil {
			return err
		}
		inserted, err := s.fiscal.Accumulators.AppendContribution(ctx, accumulator.Event{Contribution: in, Seq: next.LastSeq})
		if err != nil {
			return err
		}
		if !inserted {
			return errs.New(errs.CategoryInternal, errs.ReasonInternal,
				fmt.Sprintf("Decision %s had already contributed to %s.", d.ID, c.b.ref.Key))
		}
		if err := s.fiscal.Accumulators.SaveSnapshot(ctx, next); err != nil {
			return err
		}
		for _, x := range crossings {
			if err := s.fiscal.Accumulators.AppendCrossing(ctx, x); err != nil {
				return err
			}
			ev, err := crossingEvent(s.ids, tenant, x)
			if err != nil {
				return err
			}
			if err := s.fiscal.Outbox.Append(ctx, ev); err != nil {
				return err
			}
		}
		held[c.b.ref.Key] = next
	}
	return nil
}

func crossingEvent(ids idgen.Generator, tenant id.TenantID, x accumulator.Crossing) (outbox.Event, error) {
	eid, err := idgen.OutboxID(ids)
	if err != nil {
		return outbox.Event{}, internal(err, "The threshold crossing could not be published.")
	}
	return outbox.Event{
		ID: eid, TenantID: tenant, AggregateKey: x.Key.String(),
		Type: EventThresholdCrossed, SchemaRef: SchemaThresholdCrossedRef,
		Payload: canonical.Object(
			canonical.F("accumulatorKey", canonical.String(x.Key.String())),
			canonical.F("thresholdId", canonical.String(string(x.Threshold.ID))),
			canonical.F("limit", canonical.String(x.Threshold.Limit.CanonicalString())),
			canonical.F("comparison", canonical.String(string(x.Threshold.Comparison))),
			canonical.F("currency", canonical.String(string(x.After.Currency()))),
			canonical.F("before", canonical.String(x.Before.CanonicalString())),
			canonical.F("after", canonical.String(x.After.CanonicalString())),
			canonical.F("seq", canonical.Integer(x.Seq)),
			canonical.F("sourceDecisionId", canonical.String(x.SourceDecisionID.String())),
			canonical.F("recordedAt", canonical.Time(x.RecordedAt)),
		),
		CreatedAt: x.RecordedAt,
	}, nil
}

// post writes the journals a decision's posting declaration produces: one per
// currency among the declared slots, each balanced, for the tenant's default
// legal entity. A decision whose declared amounts are all zero posts nothing.
func (s *DeterminationService) post(ctx context.Context, tenant id.TenantID, b *rule.Bundle, d evidence.Decision) error {
	p := b.Fiscal()
	if p == nil || p.Posting == nil {
		return nil
	}
	le, err := s.defaultLegalEntity(ctx)
	if err != nil {
		return err
	}
	journals, err := journalsFor(s.ids, tenant, le, b, p.Posting, d)
	if err != nil {
		return err
	}
	for _, j := range journals {
		if j, err = guardPosting(ctx, s.fiscal.Periods, j); err != nil {
			return err
		}
		if err := s.fiscal.Journals.Append(ctx, j); err != nil {
			return err
		}
	}
	return nil
}

func journalsFor(ids idgen.Generator, tenant id.TenantID, le identity.LegalEntity, b *rule.Bundle,
	decl *content.PostingDeclaration, d evidence.Decision) ([]subledger.Journal, error) {
	byCurrency := map[fiscal.Currency][]content.PostingLineDeclaration{}
	amounts := map[string]fiscal.Money{}
	for _, l := range decl.Lines {
		v, ok := d.Result.Emitted[l.Emitted]
		if !ok || v.Type != rule.TypeMoney {
			continue
		}
		amounts["emitted:"+l.Emitted] = v.Money
		byCurrency[v.Money.Currency()] = append(byCurrency[v.Money.Currency()], l)
	}
	currencies := make([]fiscal.Currency, 0, len(byCurrency))
	for c := range byCurrency {
		currencies = append(currencies, c)
	}
	sort.Slice(currencies, func(i, j int) bool { return currencies[i] < currencies[j] })

	var out []subledger.Journal
	decisionID := d.ID
	for _, cur := range currencies {
		var rules []subledger.PostingRule
		nonZero := false
		for _, l := range byCurrency[cur] {
			rules = append(rules, subledger.PostingRule{
				Account: subledger.Account(l.Account), Side: subledger.Side(l.Side), Amount: "emitted:" + l.Emitted,
			})
			if !amounts["emitted:"+l.Emitted].IsZero() {
				nonZero = true
			}
		}
		if !nonZero {
			continue
		}
		profile := subledger.PostingProfile{
			Ref: subledger.ProfileRef{ID: decl.Profile, Version: decl.Version}, TenantID: tenant, LegalEntity: le.ID,
			Approval: "bundle:" + b.Digest(),
			Rules:    map[string]subledger.PostingProfileEntry{SourceDecisionCommitted: {Type: subledger.JournalType(decl.Type), Lines: rules}},
		}
		jid, err := idgen.JournalID(ids)
		if err != nil {
			return nil, internal(err, "The decision could not be posted.")
		}
		j, err := profile.Post(subledger.PostingEvent{
			Source:      subledger.SourceEvent{Kind: SourceDecisionCommitted, ID: d.ID.String()},
			EventTime:   d.Envelope.EventTime,
			LegalPeriod: d.Envelope.EventTime.UTC().Format("2006-01"),
			Currency:    cur, Amounts: amounts, Decision: &decisionID,
		}, jid, d.Envelope.DecisionTime)
		if err != nil {
			return nil, errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal,
				"The content's posting declaration produced a journal that does not post.")
		}
		out = append(out, j)
	}
	return out, nil
}

// reversePosting reverses every journal a superseded decision posted, under
// the correction as its source event.
func (s *DeterminationService) reversePosting(ctx context.Context, prior id.DecisionID, at time.Time) error {
	journals, err := s.fiscal.Journals.BySource(ctx, SourceDecisionCommitted, prior.String())
	if err != nil {
		return err
	}
	for _, j := range journals {
		rid, err := idgen.JournalID(s.ids)
		if err != nil {
			return internal(err, "The superseded decision's posting could not be reversed.")
		}
		r, err := j.Reverse(rid, at, subledger.SourceEvent{Kind: SourceDecisionSuperseded, ID: prior.String()})
		if err != nil {
			return err
		}
		// A reversal keeps the reversed journal's legal period
		// (ZTAX-FIN-REQ-0095), so correcting a decision whose period is
		// closed takes that period's amendment window.
		if r, err = guardPosting(ctx, s.fiscal.Periods, r); err != nil {
			return err
		}
		if err := s.fiscal.Journals.Append(ctx, r); err != nil {
			return err
		}
	}
	return nil
}
