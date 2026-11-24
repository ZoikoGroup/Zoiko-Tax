package app

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/entitlement"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/legal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/market"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ontology"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subscription"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Commercial entitlements and subscriptions (Engineering Doc 18, G-004,
// G-049). The rules are internal/domain/entitlement's and
// internal/domain/subscription's; this is where a grant or a plan meets the
// people acting, the tenant's legal entities, the cell's verified coverage
// and the store.
//
// The service holds the entitlement and usage repositories, the legal
// entities, and read-only views of coverage: the research catalogue, the
// content in force, the cell's authority and the legal gate. It holds no
// decision, rule or content writer, so no grant, plan or count can change a
// tax result or make a market live (Doc 18 §01 scope boundary, T18-04).
//
// Roles. Every role reads: a customer sees its exact entitlement, mode, date
// and the reasons it is pending or blocked (Sidebar architecture §"Coverage
// is always a verified market-by-service manifest"). Only ADMIN records and
// acts, and every approval is a second ADMIN's: the four-eye rule compares
// people, so one administrator cannot both record and approve a grant, a
// resume or a plan.
//
// Intake is idempotent on the Idempotency-Key: the same key and request
// returns the record already made; the same key with a different request is
// IDEMPOTENCY_KEY_REUSE.

// EntitlementService runs the entitlement register, the subscription plans
// and the usage report.
type EntitlementService struct {
	store    port.EntitlementRepository
	usage    port.UsageRepository
	entities port.LegalEntityRepository
	tx       port.TxManager
	clock    clock.Clock
	ids      idgen.Generator

	// Coverage; see WithCoverage.
	catalogue     market.Catalogue
	content       func(time.Time) map[string][]string
	authoritative bool
	matrix        legal.Matrix
	auths         port.AuthorizationRepository
}

// NewEntitlementService wires the service.
func NewEntitlementService(store port.EntitlementRepository, usage port.UsageRepository, entities port.LegalEntityRepository,
	tx port.TxManager, clk clock.Clock, ids idgen.Generator) *EntitlementService {
	return &EntitlementService{store: store, usage: usage, entities: entities, tx: tx, clock: clk, ids: ids}
}

// WithCoverage gives the service the cell's verified coverage: the same
// research catalogue, content in force and authority the dashboard reports
// markets from, and the legal matrix and customer authorizations the filing
// gate resolves. Without it every grant's coverage is unproven, which is the
// truth about a cell that knows of no market.
func (s *EntitlementService) WithCoverage(catalogue market.Catalogue, content func(time.Time) map[string][]string,
	authoritative bool, matrix legal.Matrix, auths port.AuthorizationRepository) *EntitlementService {
	s.catalogue, s.content, s.authoritative, s.matrix, s.auths = catalogue, content, authoritative, matrix, auths
	return s
}

var (
	entitlementReaders = []security.Role{security.RoleAdmin, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor}
	entitlementAdmins  = []security.Role{security.RoleAdmin}
)

// entitlementListCap bounds what a listing reads before filtering.
const entitlementListCap = 500

// UsageBasisCap bounds a basis listing.
const UsageBasisCap = 500

func (s *EntitlementService) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }

func (s *EntitlementService) inTx(ctx context.Context, fn func(context.Context) error) error {
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

// ---------------------------------------------------------------------------
// coverage
// ---------------------------------------------------------------------------

// facts are the cell's verified coverage at now, for evaluating grants.
func (s *EntitlementService) facts(ctx context.Context, now time.Time) (entitlement.Facts, []market.Market, error) {
	content := map[string][]string{}
	if s.content != nil {
		content = s.content(now)
	}
	var auths []legal.Authorization
	if s.auths != nil {
		var err error
		if auths, err = s.auths.List(ctx); err != nil {
			return entitlement.Facts{}, nil, err
		}
	}
	// A market is assessed with its filing gate as the market-level view
	// does: ready when some authority there would accept a return from the
	// tenant's default entity. A FILE or REMIT grant is held to its own
	// entity's gate below.
	authorities := map[string][]string{}
	for _, r := range s.matrix.Rules {
		if !slices.Contains(authorities[r.Country], r.Authority) {
			authorities[r.Country] = append(authorities[r.Country], r.Authority)
		}
	}
	for c := range authorities {
		slices.Sort(authorities[c])
	}
	gates := map[string]entitlement.FilingGate{}
	gate := func(le id.LegalEntityID, country string) entitlement.FilingGate {
		key := le.String() + "/" + country
		if g, ok := gates[key]; ok {
			return g
		}
		g := entitlement.FilingGate{}
		for _, a := range authorities[country] {
			res := legal.Resolve(s.matrix, auths, legal.Action{Country: country, Authority: a, Service: legal.ServiceFile, LegalEntity: le}, now)
			if res.Allowed {
				g = entitlement.FilingGate{Ready: true, Authority: a}
				break
			}
			if g.Authority == "" {
				g.Authority, g.Reason = a, string(res.Reason)
			}
		}
		gates[key] = g
		return g
	}
	ready := map[string]bool{}
	if d, err := s.entities.Default(ctx); err == nil {
		for c := range authorities {
			ready[c] = gate(d.ID, c).Ready
		}
	} else if !errs.IsCategory(err, errs.CategoryNotFound) {
		return entitlement.Facts{}, nil, err
	}
	markets := market.Assess(market.Facts{Catalogue: s.catalogue, Content: content, FilingReady: ready, Authoritative: s.authoritative})
	byCountry := make(map[string]market.Market, len(markets))
	for _, m := range markets {
		byCountry[m.Country] = m
	}
	return entitlement.Facts{Markets: byCountry, Authoritative: s.authoritative, Filing: gate}, markets, nil
}

// GrantView is a grant read now against the cell's coverage.
type GrantView struct {
	entitlement.Grant
	Evaluation entitlement.Evaluation
	ActiveFrom time.Time
	Offers     []entitlement.Action
	AsOf       time.Time
}

func grantView(g entitlement.Grant, f entitlement.Facts, now time.Time) GrantView {
	return GrantView{Grant: g, Evaluation: entitlement.Evaluate(g, entitlement.Day(now), f), ActiveFrom: g.ActiveFrom(),
		Offers: g.Offers(), AsOf: now}
}

// EntitlementList is the register as of now, beside the coverage it was
// read against.
type EntitlementList struct {
	Grants        []GrantView
	Markets       []market.Market
	Catalogue     string
	Authoritative bool
	AsOf          time.Time
}

// EntitlementFilter narrows a listing.
type EntitlementFilter struct {
	entitlement.Filter
	Effective entitlement.Effective
	// Mismatch keeps only approved grants over unproven coverage.
	Mismatch bool
}

// ---------------------------------------------------------------------------
// grants
// ---------------------------------------------------------------------------

// Entitlements lists grants as of now, newest first, each evaluated against
// the cell's verified coverage, and the markets that coverage reports.
func (s *EntitlementService) Entitlements(ctx context.Context, f EntitlementFilter, limit int) (EntitlementList, error) {
	if _, err := requireRoleOrSystem(ctx, entitlementReaders...); err != nil {
		return EntitlementList{}, err
	}
	now := s.now()
	facts, markets, err := s.facts(ctx, now)
	if err != nil {
		return EntitlementList{}, err
	}
	all, err := s.store.Grants(ctx, entitlementListCap)
	if err != nil {
		return EntitlementList{}, err
	}
	if limit <= 0 || limit > entitlementListCap {
		limit = entitlementListCap
	}
	day := entitlement.Day(now)
	out := EntitlementList{Grants: []GrantView{}, Markets: markets, Catalogue: s.catalogue.Version, Authoritative: s.authoritative, AsOf: now}
	for _, g := range all {
		if !f.Match(g, day) || len(out.Grants) >= limit {
			continue
		}
		v := grantView(g, facts, now)
		if (f.Effective != "" && v.Evaluation.Effective != f.Effective) || (f.Mismatch && !v.Evaluation.Mismatch) {
			continue
		}
		out.Grants = append(out.Grants, v)
	}
	return out, nil
}

// Entitlement reads one grant as of now.
func (s *EntitlementService) Entitlement(ctx context.Context, grantID id.EntitlementID) (GrantView, error) {
	if _, err := requireRoleOrSystem(ctx, entitlementReaders...); err != nil {
		return GrantView{}, err
	}
	g, err := s.store.Grant(ctx, grantID)
	if err != nil {
		return GrantView{}, err
	}
	now := s.now()
	facts, _, err := s.facts(ctx, now)
	if err != nil {
		return GrantView{}, err
	}
	return grantView(g, facts, now), nil
}

// EntitlementInput is a grant to record.
type EntitlementInput struct {
	IntakeKey        string
	LegalEntity      id.LegalEntityID // zero for the tenant's default entity
	Jurisdiction     string
	Service          ontology.Family
	Mode             entitlement.Mode
	Operation        entitlement.Operation
	EffectiveFrom    time.Time
	EffectiveTo      time.Time
	ContractRef      string
	ContractRevision string
	// Submit submits it for review as it is recorded; otherwise it is kept
	// as a DRAFT.
	Submit bool
}

func (s *EntitlementService) entity(ctx context.Context, le id.LegalEntityID) (id.LegalEntityID, error) {
	if le.IsZero() {
		d, err := s.entities.Default(ctx)
		if err != nil {
			return id.LegalEntityID{}, err
		}
		return d.ID, nil
	}
	got, err := s.entities.ByID(ctx, le)
	if err != nil {
		if errs.IsCategory(err, errs.CategoryNotFound) {
			return id.LegalEntityID{}, errs.Invalid("legalEntityId", errs.ReasonInvalidValue, "That is not one of the tenant's legal entities.")
		}
		return id.LegalEntityID{}, err
	}
	return got.ID, nil
}

// CreateEntitlement records a grant as a DRAFT, or IN_REVIEW — never
// APPROVED. created is false when the Idempotency-Key already recorded this
// request.
func (s *EntitlementService) CreateEntitlement(ctx context.Context, in EntitlementInput) (GrantView, bool, error) {
	sc, err := requirePerson(ctx, entitlementAdmins...)
	if err != nil {
		return GrantView{}, false, err
	}
	key := strings.TrimSpace(in.IntakeKey)
	if err := checkIntakeKey(key); err != nil {
		return GrantView{}, false, err
	}
	var (
		out     entitlement.Grant
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		le, err := s.entity(ctx, in.LegalEntity)
		if err != nil {
			return err
		}
		g := entitlement.Grant{
			TenantID: sc.Tenant(), LegalEntity: le, Jurisdiction: strings.ToUpper(strings.TrimSpace(in.Jurisdiction)),
			Service: in.Service, Mode: in.Mode, Operation: in.Operation, ContractRef: strings.TrimSpace(in.ContractRef),
			ContractRevision: strings.TrimSpace(in.ContractRevision), IntakeKey: key, CreatedBy: sc.Subject(),
		}
		if !in.EffectiveFrom.IsZero() {
			g.EffectiveFrom = entitlement.Day(in.EffectiveFrom)
		}
		if !in.EffectiveTo.IsZero() {
			g.EffectiveTo = entitlement.Day(in.EffectiveTo)
		}
		if err := g.Validate(); err != nil {
			return err
		}
		if g.IntakeHash, err = canonical.Sum(g.Payload(in.Submit)); err != nil {
			return internal(err, "The grant could not be hashed.")
		}
		if err := s.store.Lock(ctx, "intake/"+key); err != nil {
			return err
		}
		if prior, found, err := s.store.GrantByIntakeKey(ctx, key); err != nil {
			return err
		} else if found {
			if !prior.IntakeHash.Equal(g.IntakeHash) {
				return keyReuse()
			}
			out = prior
			return nil
		}
		gid, err := idgen.EntitlementID(s.ids)
		if err != nil {
			return internal(err, "The grant could not be recorded.")
		}
		now := s.now()
		g.ID, g.CreatedAt = gid, now
		events, err := entitlement.Open(g, in.Submit, sc.Subject(), now)
		if err != nil {
			return err
		}
		if err := s.store.CreateGrant(ctx, g, events); err != nil {
			return err
		}
		created = true
		out, err = s.store.Grant(ctx, gid)
		return err
	})
	if err != nil {
		return GrantView{}, false, err
	}
	now := s.now()
	facts, _, err := s.facts(ctx, now)
	if err != nil {
		return GrantView{}, false, err
	}
	return grantView(out, facts, now), created, nil
}

// ActOnEntitlement takes one action on a grant, against the version read.
func (s *EntitlementService) ActOnEntitlement(ctx context.Context, grantID id.EntitlementID, act entitlement.Act) (GrantView, error) {
	sc, err := requirePerson(ctx, entitlementAdmins...)
	if err != nil {
		return GrantView{}, err
	}
	var out entitlement.Grant
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "grant/"+grantID.String()); err != nil {
			return err
		}
		g, err := s.store.Grant(ctx, grantID)
		if err != nil {
			return err
		}
		var others []entitlement.Grant
		if act.Action == entitlement.ActApprove {
			// One scope's approvals are serialized, so two overlapping grants
			// approved at once cannot both pass the overlap rule.
			if err := s.store.Lock(ctx, scopeKey(g)); err != nil {
				return err
			}
			if others, err = s.store.Grants(ctx, entitlementListCap); err != nil {
				return err
			}
		}
		e, err := g.Apply(act, others, sc.Subject(), s.now())
		if err != nil {
			return err
		}
		if err := s.store.AppendGrantEvents(ctx, grantID, []entitlement.Event{e}); err != nil {
			return err
		}
		out, err = s.store.Grant(ctx, grantID)
		return err
	})
	if err != nil {
		return GrantView{}, err
	}
	now := s.now()
	facts, _, err := s.facts(ctx, now)
	if err != nil {
		return GrantView{}, err
	}
	return grantView(out, facts, now), nil
}

func scopeKey(g entitlement.Grant) string {
	return fmt.Sprintf("scope/%s/%s/%s/%s/%s", g.LegalEntity, g.Jurisdiction, g.Service, g.Mode, g.Operation)
}

// ---------------------------------------------------------------------------
// subscription plans
// ---------------------------------------------------------------------------

// PlanView is a plan revision as of now.
type PlanView struct {
	subscription.Plan
	// InForce is whether this is the plan in force today.
	InForce bool
	AsOf    time.Time
}

// PlanList is every plan revision, newest first, and which one is in force.
type PlanList struct {
	Plans   []PlanView
	InForce *PlanView
	AsOf    time.Time
}

func (s *EntitlementService) planViews(plans []subscription.Plan, now time.Time) PlanList {
	out := PlanList{Plans: make([]PlanView, len(plans)), AsOf: now}
	current, ok := subscription.InForce(plans, entitlement.Day(now))
	for i, p := range plans {
		out.Plans[i] = PlanView{Plan: p, InForce: ok && p.ID == current.ID, AsOf: now}
		if out.Plans[i].InForce {
			v := out.Plans[i]
			out.InForce = &v
		}
	}
	return out
}

// Plans lists the tenant's plan revisions, newest first.
func (s *EntitlementService) Plans(ctx context.Context) (PlanList, error) {
	if _, err := requireRoleOrSystem(ctx, entitlementReaders...); err != nil {
		return PlanList{}, err
	}
	plans, err := s.store.Plans(ctx, entitlementListCap)
	if err != nil {
		return PlanList{}, err
	}
	return s.planViews(plans, s.now()), nil
}

// Plan reads one plan revision.
func (s *EntitlementService) Plan(ctx context.Context, planID id.SubscriptionPlanID) (PlanView, error) {
	if _, err := requireRoleOrSystem(ctx, entitlementReaders...); err != nil {
		return PlanView{}, err
	}
	return s.planView(ctx, planID)
}

func (s *EntitlementService) planView(ctx context.Context, planID id.SubscriptionPlanID) (PlanView, error) {
	plans, err := s.store.Plans(ctx, entitlementListCap)
	if err != nil {
		return PlanView{}, err
	}
	list := s.planViews(plans, s.now())
	for _, v := range list.Plans {
		if v.ID == planID {
			return v, nil
		}
	}
	// Older than the listing reaches: read it alone; it is not in force.
	p, err := s.store.Plan(ctx, planID)
	if err != nil {
		return PlanView{}, err
	}
	return PlanView{Plan: p, AsOf: list.AsOf}, nil
}

// PlanInput is a plan revision to propose.
type PlanInput struct {
	IntakeKey   string
	PlanCode    string
	PlanName    string
	ContractRef string
	TermStart   time.Time
	TermEnd     time.Time
	Quotas      []subscription.Quota
}

// ProposePlan records a plan revision as PROPOSED. It is in force only once
// a second administrator approves it. created is false when the
// Idempotency-Key already recorded this request.
func (s *EntitlementService) ProposePlan(ctx context.Context, in PlanInput) (PlanView, bool, error) {
	sc, err := requirePerson(ctx, entitlementAdmins...)
	if err != nil {
		return PlanView{}, false, err
	}
	key := strings.TrimSpace(in.IntakeKey)
	if err := checkIntakeKey(key); err != nil {
		return PlanView{}, false, err
	}
	var (
		planID  id.SubscriptionPlanID
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		p := subscription.Plan{
			TenantID: sc.Tenant(), PlanCode: strings.ToUpper(strings.TrimSpace(in.PlanCode)), PlanName: strings.TrimSpace(in.PlanName),
			ContractRef: strings.TrimSpace(in.ContractRef), Quotas: slices.Clone(in.Quotas), IntakeKey: key, CreatedBy: sc.Subject(),
		}
		if !in.TermStart.IsZero() {
			p.TermStart = entitlement.Day(in.TermStart)
		}
		if !in.TermEnd.IsZero() {
			p.TermEnd = entitlement.Day(in.TermEnd)
		}
		// Quotas are stored and hashed in meter order, so the same plan typed
		// in another order is the same request.
		slices.SortFunc(p.Quotas, func(a, b subscription.Quota) int { return cmp.Compare(a.Meter, b.Meter) })
		if err := p.Validate(); err != nil {
			return err
		}
		var err error
		if p.IntakeHash, err = canonical.Sum(p.Payload()); err != nil {
			return internal(err, "The plan could not be hashed.")
		}
		if err := s.store.Lock(ctx, "plan-intake/"+key); err != nil {
			return err
		}
		if prior, found, err := s.store.PlanByIntakeKey(ctx, key); err != nil {
			return err
		} else if found {
			if !prior.IntakeHash.Equal(p.IntakeHash) {
				return keyReuse()
			}
			planID = prior.ID
			return nil
		}
		if p.ID, err = idgen.SubscriptionPlanID(s.ids); err != nil {
			return internal(err, "The plan could not be recorded.")
		}
		now := s.now()
		p.CreatedAt = now
		events, err := subscription.Open(p, sc.Subject(), now)
		if err != nil {
			return err
		}
		if err := s.store.CreatePlan(ctx, p, events); err != nil {
			return err
		}
		planID, created = p.ID, true
		return nil
	})
	if err != nil {
		return PlanView{}, false, err
	}
	v, err := s.planView(ctx, planID)
	return v, created, err
}

// ActOnPlan approves, rejects or withdraws a proposed plan revision.
func (s *EntitlementService) ActOnPlan(ctx context.Context, planID id.SubscriptionPlanID, act subscription.Action,
	expectedVersion int, reason string) (PlanView, error) {
	sc, err := requirePerson(ctx, entitlementAdmins...)
	if err != nil {
		return PlanView{}, err
	}
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "plan/"+planID.String()); err != nil {
			return err
		}
		p, err := s.store.Plan(ctx, planID)
		if err != nil {
			return err
		}
		e, err := p.Apply(act, expectedVersion, reason, sc.Subject(), s.now())
		if err != nil {
			return err
		}
		return s.store.AppendPlanEvent(ctx, planID, e)
	})
	if err != nil {
		return PlanView{}, err
	}
	return s.planView(ctx, planID)
}

// ---------------------------------------------------------------------------
// usage
// ---------------------------------------------------------------------------

// UsageReport is one period's metered usage against the plan in force.
type UsageReport struct {
	Window subscription.Window
	// Complete is whether the period has ended: an open period's counts
	// grow until it does.
	Complete bool
	// Plan is the plan the period is measured against: the one in force on
	// the period's last day, or today for the open period. Nil for none.
	Plan               *subscription.Plan
	Lines              []subscription.Line
	DefinitionsVersion string
	AsOf               time.Time
}

func (s *EntitlementService) usageWindow(period string, now time.Time) (subscription.Window, error) {
	if period == "" {
		period = subscription.PeriodOf(now)
	}
	w, err := subscription.PeriodWindow(period)
	if err != nil {
		return w, err
	}
	if w.From.After(now) {
		return w, errs.Invalid("period", errs.ReasonInvalidValue, "That period has not started; nothing has been metered in it.")
	}
	return w, nil
}

// Usage counts one period's usage from the cell's own records and lines it
// up against the plan in force. Nothing is stored: asking again recounts,
// and a closed period always recounts to the same numbers and digests.
func (s *EntitlementService) Usage(ctx context.Context, period string) (UsageReport, error) {
	if _, err := requireRoleOrSystem(ctx, entitlementReaders...); err != nil {
		return UsageReport{}, err
	}
	now := s.now()
	w, err := s.usageWindow(period, now)
	if err != nil {
		return UsageReport{}, err
	}
	counts, err := s.usage.Counts(ctx, w.From, w.To)
	if err != nil {
		return UsageReport{}, err
	}
	plans, err := s.store.Plans(ctx, entitlementListCap)
	if err != nil {
		return UsageReport{}, err
	}
	out := UsageReport{Window: w, Complete: !w.To.After(now), DefinitionsVersion: subscription.DefinitionsVersion, AsOf: now}
	measured := w.To.Add(-time.Microsecond)
	if now.Before(measured) {
		measured = now
	}
	if p, ok := subscription.InForce(plans, entitlement.Day(measured)); ok {
		out.Plan = &p
	}
	out.Lines = subscription.Assess(counts, out.Plan)
	return out, nil
}

// UsageBasis is the rows one meter counted in a period: the count and
// digest over all of them, and the first of them in digest order.
type UsageBasis struct {
	Window     subscription.Window
	Definition subscription.Definition
	Count      subscription.Count
	Rows       []port.UsageBasisRow
	// Truncated is whether Rows stops short of Count.
	Truncated bool
	AsOf      time.Time
}

// Basis lists what a meter counted, so a count can be checked row by row.
func (s *EntitlementService) Basis(ctx context.Context, period string, m subscription.Meter, limit int) (UsageBasis, error) {
	if _, err := requireRoleOrSystem(ctx, entitlementReaders...); err != nil {
		return UsageBasis{}, err
	}
	def, ok := subscription.DefinitionOf(m)
	if !ok {
		return UsageBasis{}, errs.Invalid("meter", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a meter.", m))
	}
	now := s.now()
	w, err := s.usageWindow(period, now)
	if err != nil {
		return UsageBasis{}, err
	}
	if limit <= 0 || limit > UsageBasisCap {
		limit = UsageBasisCap
	}
	out := UsageBasis{Window: w, Definition: def, AsOf: now}
	if out.Count, out.Rows, err = s.usage.Basis(ctx, m, w.From, w.To, limit); err != nil {
		return UsageBasis{}, err
	}
	if out.Rows == nil {
		out.Rows = []port.UsageBasisRow{}
	}
	out.Truncated = int64(len(out.Rows)) < out.Count.Count
	return out, nil
}
