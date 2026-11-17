package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	pcontrib "github.com/zoikogroup/zoikotax/backend/internal/domain/contribution"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Provider contributions (OBL-06, OBL-10). The rules are
// internal/domain/contribution's; this is where a source event meets its
// member entity, the events already recorded and the store.
//
// A provider contribution is the operator's own obligation, assessed on its
// eligible revenue. Nothing here reads a tax decision or a customer
// surcharge line, so recovery charged to end users can never be counted as
// the contribution payable (UAT-OBL-032, -033).
//
// Idempotency is natural rather than keyed: the source system already names
// each event and its version, and that pair is the record's identity. A
// client-minted Idempotency-Key would be a second identity for the same
// thing, and two keys for one source event would record it twice — exactly
// what UAT-OBL-026 forbids.

// ProviderContributionService records, explores and totals provider
// contributions.
type ProviderContributionService struct {
	store    port.ProviderContributionRepository
	entities port.LegalEntityRepository
	tx       port.TxManager
	clock    clock.Clock
	ids      idgen.Generator
}

// NewProviderContributionService wires the service.
func NewProviderContributionService(store port.ProviderContributionRepository, entities port.LegalEntityRepository,
	tx port.TxManager, clk clock.Clock, ids idgen.Generator) *ProviderContributionService {
	return &ProviderContributionService{store: store, entities: entities, tx: tx, clock: clk, ids: ids}
}

func (s *ProviderContributionService) now() time.Time {
	return s.clock.Now().UTC().Truncate(time.Microsecond)
}

// The roles. OBL-06 names Tax Ops, Data Engineer and Auditor: operators
// record and quarantine, analysts quarantine and release, and all three
// fiscal roles read. ADMIN reads no fiscal record here, as elsewhere.
//
// Release is four-eyed: an analyst who did not quarantine the event releases
// it, so a hold on a suspect import is never lifted by the person who placed
// it alone.
var (
	contributionReaders      = []security.Role{security.RoleOperator, security.RoleAnalyst, security.RoleAuditor}
	contributionRecorders    = []security.Role{security.RoleOperator}
	contributionQuarantiners = []security.Role{security.RoleOperator, security.RoleAnalyst}
	contributionReleasers    = []security.Role{security.RoleAnalyst}
)

// ContributionInput is one source event to record. Amounts are canonical
// decimal strings, parsed here and nowhere else.
type ContributionInput struct {
	SourceEventID     string
	SourceVersion     int
	SourceType        pcontrib.SourceType
	SourceChecksum    string
	Regime            pcontrib.Regime
	Period            string
	LegalEntity       id.LegalEntityID // zero for the tenant's default entity
	Obligation        id.ObligationID  // zero for none
	ClassificationRef string
	SitusRef          string
	SourceRef         string
	ComponentRef      string
	RuleVersion       string
	Currency          string
	EligibleBase      string
	ExcludedBase      string
	ExclusionReason   string
	Quantity          string // empty for none
	QuantityUnit      string
	Reverses          *pcontrib.SourceKey
}

// ContributionTrace is one contribution with what it is linked to: the
// original it reverses, when the ledger holds it, and the reversal of it,
// when one has been recorded.
type ContributionTrace struct {
	Contribution pcontrib.Contribution
	Original     *pcontrib.Contribution
	Reversal     *pcontrib.Contribution
}

// PeriodSummary is a period's control totals.
type PeriodSummary struct {
	Period string
	Groups []pcontrib.Totals
}

// DataIncomplete reports whether any group is DATA_INCOMPLETE.
func (p PeriodSummary) DataIncomplete() bool {
	for _, g := range p.Groups {
		if g.DataIncomplete() {
			return true
		}
	}
	return false
}

func (s *ProviderContributionService) build(ctx context.Context, sc security.Context, in ContributionInput) (pcontrib.Contribution, error) {
	cur := strings.ToUpper(strings.TrimSpace(in.Currency))
	eligible, err := pcontrib.ParseAmount("eligibleBase", strings.TrimSpace(in.EligibleBase), cur)
	if err != nil {
		return pcontrib.Contribution{}, err
	}
	excludedText := strings.TrimSpace(in.ExcludedBase)
	if excludedText == "" {
		excludedText = "0"
	}
	excluded, err := pcontrib.ParseAmount("excludedBase", excludedText, cur)
	if err != nil {
		return pcontrib.Contribution{}, err
	}
	c := pcontrib.Contribution{
		TenantID:          sc.Tenant(),
		Source:            pcontrib.SourceKey{EventID: strings.TrimSpace(in.SourceEventID), Version: in.SourceVersion},
		SourceType:        in.SourceType,
		SourceChecksum:    strings.TrimSpace(in.SourceChecksum),
		Regime:            in.Regime,
		Period:            strings.TrimSpace(in.Period),
		Obligation:        in.Obligation,
		ClassificationRef: strings.TrimSpace(in.ClassificationRef),
		SitusRef:          strings.TrimSpace(in.SitusRef),
		SourceRef:         strings.TrimSpace(in.SourceRef),
		ComponentRef:      strings.TrimSpace(in.ComponentRef),
		RuleVersion:       strings.TrimSpace(in.RuleVersion),
		EligibleBase:      eligible,
		ExcludedBase:      excluded,
		ExclusionReason:   strings.TrimSpace(in.ExclusionReason),
		RecordedBy:        sc.Subject(),
	}
	if q := strings.TrimSpace(in.Quantity); q != "" {
		unit := strings.TrimSpace(in.QuantityUnit)
		if unit == "" {
			return pcontrib.Contribution{}, errs.Invalid("quantityUnit", errs.ReasonMissingField, "A quantity names its unit.")
		}
		parsed, err := fiscal.ParseQuantity(q, fiscal.Unit(unit))
		if err != nil {
			return pcontrib.Contribution{}, errs.Invalid("quantity", errs.ReasonInvalidValue, "The quantity is a canonical decimal string.")
		}
		c.Quantity = &parsed
	}
	if in.Reverses != nil {
		k := pcontrib.SourceKey{EventID: strings.TrimSpace(in.Reverses.EventID), Version: in.Reverses.Version}
		c.Reverses = &k
	}
	if in.LegalEntity.IsZero() {
		le, err := s.entities.Default(ctx)
		if err != nil {
			return pcontrib.Contribution{}, err
		}
		c.LegalEntity = le.ID
	} else {
		le, err := s.entities.ByID(ctx, in.LegalEntity)
		if err != nil {
			return pcontrib.Contribution{}, err
		}
		c.LegalEntity = le.ID
	}
	if err := c.Validate(); err != nil {
		return pcontrib.Contribution{}, err
	}
	if err := c.Seal(); err != nil {
		return pcontrib.Contribution{}, internal(err, "The contribution could not be hashed.")
	}
	return c, nil
}

// Record records one source event as a contribution. It is idempotent on
// the source event and version: the same payload again returns the record
// already made, with created false, and a different payload is refused with
// SOURCE_EVENT_CONFLICT (UAT-OBL-026). A reversal is recorded as its own
// negative contribution naming the original's source key (UAT-OBL-027);
// each original is reversed at most once.
func (s *ProviderContributionService) Record(ctx context.Context, in ContributionInput) (pcontrib.Contribution, bool, error) {
	sc, err := requirePerson(ctx, contributionRecorders...)
	if err != nil {
		return pcontrib.Contribution{}, false, err
	}
	var (
		out     pcontrib.Contribution
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		c, err := s.build(ctx, sc, in)
		if err != nil {
			return err
		}
		if err := s.store.Lock(ctx, "source/"+c.Source.String()); err != nil {
			return err
		}
		prior, found, err := s.store.BySource(ctx, c.Source)
		if err != nil {
			return err
		}
		if found {
			if !prior.SamePayload(c) {
				return errs.New(errs.CategoryConflict, pcontrib.ReasonSourceEventConflict,
					fmt.Sprintf("Source event %s was recorded on %s with a different payload. Send a new version, or a reversal.",
						c.Source, prior.RecordedAt.Format(time.DateOnly)))
			}
			out = prior
			return nil
		}
		if c.Reverses != nil {
			if err := s.store.Lock(ctx, "reversal-of/"+c.Reverses.String()); err != nil {
				return err
			}
			if other, found, err := s.store.ReversalOf(ctx, *c.Reverses); err != nil {
				return err
			} else if found {
				return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists,
					fmt.Sprintf("Source event %s is already reversed by %s.", c.Reverses, other.Source))
			}
			original, found, err := s.store.BySource(ctx, *c.Reverses)
			if err != nil {
				return err
			}
			// An original not yet in the ledger is not a refusal: the
			// reversal is recorded, and the period stays DATA_INCOMPLETE
			// until the original arrives.
			if found {
				if err := c.CheckReversal(original); err != nil {
					return err
				}
			}
		}
		cid, err := idgen.ContributionID(s.ids)
		if err != nil {
			return internal(err, "The contribution could not be recorded.")
		}
		now := s.now()
		c.ID, c.RecordedAt = cid, now
		c.Amendment = pcontrib.AmendmentAt(c.Period, now)
		if err := s.store.Record(ctx, c, pcontrib.Recorded(sc.Subject(), now)); err != nil {
			return err
		}
		created = true
		out, err = s.store.Contribution(ctx, cid)
		return err
	})
	return out, created, err
}

// Contributions lists contributions, newest first.
func (s *ProviderContributionService) Contributions(ctx context.Context, f pcontrib.Filter) ([]pcontrib.Contribution, error) {
	if _, err := requireRoleOrSystem(ctx, contributionReaders...); err != nil {
		return nil, err
	}
	if f.Period != "" && !pcontrib.ValidPeriod(f.Period) {
		return nil, errs.Invalid("period", errs.ReasonInvalidValue, "The period is a month, YYYY-MM.")
	}
	return s.store.List(ctx, f)
}

// Trace reads one contribution with its history and its links: the
// original it reverses and the reversal of it.
func (s *ProviderContributionService) Trace(ctx context.Context, contributionID id.ContributionID) (ContributionTrace, error) {
	if _, err := requireRoleOrSystem(ctx, contributionReaders...); err != nil {
		return ContributionTrace{}, err
	}
	c, err := s.store.Contribution(ctx, contributionID)
	if err != nil {
		return ContributionTrace{}, err
	}
	out := ContributionTrace{Contribution: c}
	if c.Reverses != nil {
		o, found, err := s.store.BySource(ctx, *c.Reverses)
		if err != nil {
			return ContributionTrace{}, err
		}
		if found {
			out.Original = &o
		}
	}
	r, found, err := s.store.ReversalOf(ctx, c.Source)
	if err != nil {
		return ContributionTrace{}, err
	}
	if found {
		out.Reversal = &r
	}
	return out, nil
}

// Quarantine holds an accepted contribution out of the control totals with
// its reason. The contribution is not deleted, and its history keeps the
// hold.
func (s *ProviderContributionService) Quarantine(ctx context.Context, contributionID id.ContributionID, reason string) (pcontrib.Contribution, error) {
	sc, err := requirePerson(ctx, contributionQuarantiners...)
	if err != nil {
		return pcontrib.Contribution{}, err
	}
	return s.move(ctx, contributionID, func(c pcontrib.Contribution) (pcontrib.StatusEvent, error) {
		return c.Quarantine(strings.TrimSpace(reason), sc.Subject(), s.now())
	})
}

// Release returns a quarantined contribution to the control totals. The
// person who quarantined it does not release it.
func (s *ProviderContributionService) Release(ctx context.Context, contributionID id.ContributionID, reason string) (pcontrib.Contribution, error) {
	sc, err := requirePerson(ctx, contributionReleasers...)
	if err != nil {
		return pcontrib.Contribution{}, err
	}
	return s.move(ctx, contributionID, func(c pcontrib.Contribution) (pcontrib.StatusEvent, error) {
		return c.Release(strings.TrimSpace(reason), sc.Subject(), s.now())
	})
}

func (s *ProviderContributionService) move(ctx context.Context, contributionID id.ContributionID,
	next func(pcontrib.Contribution) (pcontrib.StatusEvent, error)) (pcontrib.Contribution, error) {
	var out pcontrib.Contribution
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "id/"+contributionID.String()); err != nil {
			return err
		}
		c, err := s.store.Contribution(ctx, contributionID)
		if err != nil {
			return err
		}
		e, err := next(c)
		if err != nil {
			return err
		}
		if err := s.store.AppendEvent(ctx, c.ID, e); err != nil {
			return err
		}
		out, err = s.store.Contribution(ctx, c.ID)
		return err
	})
	return out, err
}

// Summary computes a period's control totals per member entity, regime and
// currency: counts, exact totals with reversals as negatives, the period
// contribution digest, and DATA_INCOMPLETE when an event is quarantined or a
// reversal names an original the ledger does not hold.
func (s *ProviderContributionService) Summary(ctx context.Context, period string) (PeriodSummary, error) {
	if _, err := requireRoleOrSystem(ctx, contributionReaders...); err != nil {
		return PeriodSummary{}, err
	}
	if !pcontrib.ValidPeriod(period) {
		return PeriodSummary{}, errs.Invalid("period", errs.ReasonInvalidValue, "The period is a month, YYYY-MM.")
	}
	events, err := s.store.InPeriod(ctx, period)
	if err != nil {
		return PeriodSummary{}, err
	}
	var keys []pcontrib.SourceKey
	for _, c := range events {
		if c.Reverses != nil {
			keys = append(keys, *c.Reverses)
		}
	}
	known, err := s.store.KnownSources(ctx, keys)
	if err != nil {
		return PeriodSummary{}, err
	}
	groups, err := pcontrib.Summarize(period, events, func(k pcontrib.SourceKey) bool { return known[k] })
	if err != nil {
		return PeriodSummary{}, internal(err, "The period's control totals could not be computed.")
	}
	return PeriodSummary{Period: period, Groups: groups}, nil
}

func (s *ProviderContributionService) inTx(ctx context.Context, fn func(context.Context) error) error {
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
