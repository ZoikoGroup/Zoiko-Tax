package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/shadow"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Shadow assurance (ZTAX-SHD-001, W4 lane M). The comparison rules are
// internal/domain/shadow's; this is where a comparator's imported results
// meet the decisions ZoikoTax committed.
//
// Non-impact by construction: the service holds the decision repository
// and the evidence store for reading only, and writes nothing but its own
// runs, cases and discrepancies (ZTAX-SHD-REQ-0001).

// ShadowService runs comparisons and their investigations.
type ShadowService struct {
	store     port.ShadowRepository
	decisions port.DecisionRepository
	evidence  port.EvidenceStore
	tx        port.TxManager
	clock     clock.Clock
	ids       idgen.Generator
}

// NewShadowService wires the service.
func NewShadowService(store port.ShadowRepository, decisions port.DecisionRepository, objects port.EvidenceStore,
	tx port.TxManager, clk clock.Clock, ids idgen.Generator) *ShadowService {
	return &ShadowService{store: store, decisions: decisions, evidence: objects, tx: tx, clock: clk, ids: ids}
}

func (s *ShadowService) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }

var (
	shadowReaders   = []security.Role{security.RoleAdmin, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor}
	shadowOperators = []security.Role{security.RoleAdmin, security.RoleOperator, security.RoleAnalyst}
)

// maxObservations bounds one run's import.
const maxObservations = 1000

// ComparatorInput is a comparator profile to record.
type ComparatorInput struct {
	Name        string
	Type        shadow.ComparatorType
	Provider    string
	Version     string
	Environment string
	Limitations string
}

// CreateComparator records a comparator profile.
func (s *ShadowService) CreateComparator(ctx context.Context, in ComparatorInput) (shadow.Comparator, error) {
	sc, err := requirePerson(ctx, shadowOperators...)
	if err != nil {
		return shadow.Comparator{}, err
	}
	cid, err := idgen.ComparatorID(s.ids)
	if err != nil {
		return shadow.Comparator{}, internal(err, "The comparator could not be recorded.")
	}
	version := strings.TrimSpace(in.Version)
	if version == "" {
		version = "UNKNOWN"
	}
	c := shadow.Comparator{ID: cid, TenantID: sc.Tenant(), Name: strings.TrimSpace(in.Name), Type: in.Type,
		Provider: strings.TrimSpace(in.Provider), Version: version, Environment: strings.TrimSpace(in.Environment),
		Limitations: strings.TrimSpace(in.Limitations), CreatedAt: s.now(), CreatedBy: sc.Subject()}
	if err := c.Validate(); err != nil {
		return shadow.Comparator{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue, strings.TrimPrefix(err.Error(), "shadow: ")+".")
	}
	if err := s.inTx(ctx, func(ctx context.Context) error { return s.store.CreateComparator(ctx, c) }); err != nil {
		return shadow.Comparator{}, err
	}
	return c, nil
}

// Comparators lists the tenant's comparator profiles.
func (s *ShadowService) Comparators(ctx context.Context) ([]shadow.Comparator, error) {
	if _, err := requireRoleOrSystem(ctx, shadowReaders...); err != nil {
		return nil, err
	}
	return s.store.Comparators(ctx)
}

// RunInput is a comparison to execute: a comparator's results for a window,
// and the policy to compare them under.
type RunInput struct {
	Comparator   id.ComparatorID
	Mode         shadow.Mode
	WindowFrom   time.Time
	WindowTo     time.Time
	Policy       shadow.Policy
	Observations []shadow.Observation
}

// ShadowRunView is a run with what it means: the comparator, the claim a match
// may make, its discrepancies and what blocks cutover on its evidence.
type ShadowRunView struct {
	shadow.Run
	ComparatorProfile shadow.Comparator
	Claim             shadow.Claim
	Discrepancies     []shadow.Discrepancy
	Blockers          []string
}

// ExecuteRun pairs each comparator observation with the current decision of
// its business key, compares them, records the run with its cases, and
// opens a discrepancy for every material case. Decisions in the window with
// no observation are unpaired, and reported as such.
func (s *ShadowService) ExecuteRun(ctx context.Context, in RunInput) (ShadowRunView, error) {
	sc, err := requirePerson(ctx, shadowOperators...)
	if err != nil {
		return ShadowRunView{}, err
	}
	switch {
	case !in.Mode.Valid():
		return ShadowRunView{}, errs.Invalid("mode", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a shadow mode this service runs.", in.Mode))
	case !in.WindowTo.After(in.WindowFrom):
		return ShadowRunView{}, errs.Invalid("windowTo", errs.ReasonInvalidValue, "The window ends after it starts.")
	case len(in.Observations) == 0 || len(in.Observations) > maxObservations:
		return ShadowRunView{}, errs.Invalid("observations", errs.ReasonInvalidValue,
			fmt.Sprintf("A run imports between 1 and %d comparator results.", maxObservations))
	}
	if err := in.Policy.Validate(); err != nil {
		return ShadowRunView{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue, strings.TrimPrefix(err.Error(), "shadow: ")+".")
	}
	seen := map[string]bool{}
	for _, o := range in.Observations {
		if strings.TrimSpace(o.CaseRef) == "" {
			return ShadowRunView{}, errs.Invalid("observations", errs.ReasonMissingField, "Every comparator result names its business key.")
		}
		if seen[o.CaseRef] {
			// Two results for one key is a duplicate candidate; neither is
			// paired by guess (ZTAX-SHD-001 §8).
			return ShadowRunView{}, errs.Invalid("observations", errs.ReasonInvalidValue,
				fmt.Sprintf("Business key %q appears twice; a run pairs each key once.", o.CaseRef))
		}
		seen[o.CaseRef] = true
	}
	comparator, err := s.store.Comparator(ctx, in.Comparator)
	if err != nil {
		return ShadowRunView{}, err
	}
	hash, err := inputHash(in.Observations)
	if err != nil {
		return ShadowRunView{}, internal(err, "The comparator input could not be hashed.")
	}
	rid, err := idgen.ShadowRunID(s.ids)
	if err != nil {
		return ShadowRunView{}, internal(err, "The run could not be recorded.")
	}

	var cases []shadow.Case
	for _, o := range in.Observations {
		rec, emitted, ok, err := s.current(ctx, o.CaseRef)
		if err != nil {
			return ShadowRunView{}, err
		}
		if !ok {
			total := o.Total
			cases = append(cases, shadow.Unpaired(o.CaseRef, shadow.PairingUnpairedComparator, id.DecisionID{}, &total, nil))
			continue
		}
		c, err := shadow.Compare(in.Policy, o, rec.DecisionID, emitted)
		if err != nil {
			return ShadowRunView{}, internal(err, "A comparison could not be computed.")
		}
		cases = append(cases, c)
	}
	inWindow, err := s.decisions.CurrentInWindow(ctx, in.WindowFrom, in.WindowTo)
	if err != nil {
		return ShadowRunView{}, err
	}
	for _, rec := range inWindow {
		if seen[rec.BusinessKey] {
			continue
		}
		emitted, err := s.emitted(ctx, rec)
		if err != nil {
			return ShadowRunView{}, err
		}
		total, err := sumIn(emitted, in.Policy.Currency, in.Policy.Tolerance.Zero())
		if err != nil {
			return ShadowRunView{}, err
		}
		cases = append(cases, shadow.Unpaired(rec.BusinessKey, shadow.PairingUnpairedZoiko, rec.DecisionID, nil, &total))
	}
	for i := range cases {
		cases[i].Seq = i + 1
	}
	metrics, err := shadow.Summarise(in.Policy, cases)
	if err != nil {
		return ShadowRunView{}, internal(err, "The run's metrics could not be computed.")
	}
	now := s.now()
	run := shadow.Run{ID: rid, TenantID: sc.Tenant(), Comparator: comparator.ID, Mode: in.Mode, WindowFrom: in.WindowFrom.UTC(),
		WindowTo: in.WindowTo.UTC(), Policy: in.Policy, InputHash: hash, Metrics: metrics, CreatedAt: now, CreatedBy: sc.Subject(), Cases: cases}

	var discrepancies []shadow.Discrepancy
	for _, c := range cases {
		if !c.Material {
			continue
		}
		did, err := idgen.DiscrepancyID(s.ids)
		if err != nil {
			return ShadowRunView{}, internal(err, "A discrepancy could not be opened.")
		}
		severity := shadow.SeverityTwo
		if c.State == shadow.VariancePopulation {
			severity = shadow.SeverityThree
		}
		discrepancies = append(discrepancies, shadow.Discrepancy{ID: did, TenantID: sc.Tenant(), Run: rid, CaseSeq: c.Seq,
			History: []shadow.Event{{Seq: 1, State: shadow.StateNew, Severity: severity, RootCause: "UNKNOWN",
				Note: fmt.Sprintf("Opened by the run: %s on %s.", c.State, c.CaseRef), RecordedAt: now, RecordedBy: sc.Subject()}}})
	}
	if err := s.inTx(ctx, func(ctx context.Context) error { return s.store.CreateRun(ctx, run, discrepancies) }); err != nil {
		return ShadowRunView{}, err
	}
	return s.Run(ctx, rid)
}

// Run reads one run with its cases, discrepancies and cutover blockers.
func (s *ShadowService) Run(ctx context.Context, runID id.ShadowRunID) (ShadowRunView, error) {
	if _, err := requireRoleOrSystem(ctx, shadowReaders...); err != nil {
		return ShadowRunView{}, err
	}
	run, err := s.store.Run(ctx, runID)
	if err != nil {
		return ShadowRunView{}, err
	}
	return s.view(ctx, run)
}

// Runs lists the tenant's runs, newest first, as summaries.
func (s *ShadowService) Runs(ctx context.Context, limit int) ([]ShadowRunView, error) {
	if _, err := requireRoleOrSystem(ctx, shadowReaders...); err != nil {
		return nil, err
	}
	runs, err := s.store.Runs(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ShadowRunView, 0, len(runs))
	for _, r := range runs {
		v, err := s.view(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// MoveDiscrepancy records the next step of an investigation.
func (s *ShadowService) MoveDiscrepancy(ctx context.Context, discrepancyID id.DiscrepancyID, to shadow.State,
	severity shadow.Severity, cause shadow.RootCause, note string) (shadow.Discrepancy, error) {
	sc, err := requirePerson(ctx, shadowOperators...)
	if err != nil {
		return shadow.Discrepancy{}, err
	}
	var out shadow.Discrepancy
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "discrepancy/"+discrepancyID.String()); err != nil {
			return err
		}
		d, err := s.store.Discrepancy(ctx, discrepancyID)
		if err != nil {
			return err
		}
		e, err := d.Move(to, severity, cause, strings.TrimSpace(note), sc.Subject(), s.now())
		if err != nil {
			return err
		}
		if err := s.store.AppendDiscrepancyEvent(ctx, d.ID, e); err != nil {
			return err
		}
		out, err = s.store.Discrepancy(ctx, d.ID)
		return err
	})
	return out, err
}

func (s *ShadowService) view(ctx context.Context, run shadow.Run) (ShadowRunView, error) {
	comparator, err := s.store.Comparator(ctx, run.Comparator)
	if err != nil {
		return ShadowRunView{}, err
	}
	ds, err := s.store.Discrepancies(ctx, run.ID)
	if err != nil {
		return ShadowRunView{}, err
	}
	return ShadowRunView{Run: run, ComparatorProfile: comparator, Claim: comparator.Type.Claim(), Discrepancies: ds,
		Blockers: shadow.Blockers(comparator, run.Metrics, ds)}, nil
}

// current reads the current decision of a business key and the money it
// emitted; false when ZoikoTax has no decision for it.
func (s *ShadowService) current(ctx context.Context, businessKey string) (evidence.Record, map[string]fiscal.Money, bool, error) {
	history, err := s.decisions.History(ctx, businessKey)
	if err != nil && !errs.IsCategory(err, errs.CategoryNotFound) {
		return evidence.Record{}, nil, false, err
	}
	if len(history) == 0 {
		return evidence.Record{}, nil, false, nil
	}
	rec := history[len(history)-1]
	emitted, err := s.emitted(ctx, rec)
	return rec, emitted, err == nil, err
}

// emitted is the money a decision emitted, by slot, read from its sealed
// result rather than from any index.
func (s *ShadowService) emitted(ctx context.Context, rec evidence.Record) (map[string]fiscal.Money, error) {
	data, err := s.evidence.Get(ctx, rec.ResultDigest)
	if err != nil {
		return nil, err
	}
	result, err := evidence.DecodeResult(data)
	if err != nil {
		return nil, integrity(err, "A recorded result cannot be read as canonical evidence.")
	}
	out := map[string]fiscal.Money{}
	for slot, v := range result.Emitted {
		if v.Type == rule.TypeMoney {
			out[slot] = v.Money
		}
	}
	return out, nil
}

func sumIn(emitted map[string]fiscal.Money, currency fiscal.Currency, zero fiscal.Money) (fiscal.Money, error) {
	total := zero
	for _, m := range emitted {
		if m.Currency() != currency {
			continue
		}
		next, err := total.Add(m)
		if err != nil {
			return fiscal.Money{}, internal(err, "A decision's total could not be computed.")
		}
		total = next
	}
	return total, nil
}

// inputHash fingerprints the comparator input, in business-key order, so a
// run names exactly what it was given (ZTAX-SHD-001 §6, §7).
func inputHash(obs []shadow.Observation) (canonical.Digest, error) {
	sorted := append([]shadow.Observation{}, obs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].CaseRef < sorted[j].CaseRef })
	items := make([]canonical.Value, len(sorted))
	for i, o := range sorted {
		codes := make([]string, 0, len(o.Components))
		for c := range o.Components {
			codes = append(codes, c)
		}
		sort.Strings(codes)
		comps := make([]canonical.Field, len(codes))
		for j, c := range codes {
			comps[j] = canonical.F(c, canonical.Money(o.Components[c]))
		}
		items[i] = canonical.Object(
			canonical.F("caseRef", canonical.String(o.CaseRef)),
			canonical.F("currency", canonical.String(string(o.Total.Currency()))),
			canonical.F("total", canonical.Money(o.Total)),
			canonical.F("components", canonical.Object(comps...)),
		)
	}
	return canonical.Sum(canonical.Array(items...))
}

func (s *ShadowService) inTx(ctx context.Context, fn func(context.Context) error) error {
	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(txCtx) }()
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(txCtx)
}
