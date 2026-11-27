package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/regchange"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Regulatory change intelligence (Engineering Doc 03 §04, §06, WF-05, WF-09;
// Sidebar Navigation Architecture WF-02; ZTAX-AIGOV-001). The rules are
// internal/domain/regchange's; this is where a change meets the people
// acting, the active content, the tenant's records and the store.
//
// The service reads the active content's coverage, the registration ledger
// and the obligation register, and writes only its own three ledgers. It
// holds no decision, fiscal, ledger, document, filing or content-release
// writer, so no act here — an assessment, an accepted recommendation, a
// closure — can change a tax result, a filing or a release pack (Doc 03
// Z03-AT-17; WF-02 "No direct release-pack publication").
//
// Roles. OPERATOR and ANALYST record changes (human legal intake, or a
// watcher's alert recorded by a person). ANALYST is the specialist: it
// triages, holds and decides changes, runs assessments, drafts, recommends
// and independently reviews recommendations. AUDITOR reads. ADMIN reads
// none of it: an assessment lists the tenant's own registrations and
// obligations.
//
// Change intake and recommendation drafting are idempotent on the
// Idempotency-Key. A change is also deduplicated by its source identity,
// version and kind: the same source edition's change reported again returns
// the change already recorded, and with different particulars is
// CHANGE_DEDUPE_CONFLICT (Doc 03 §08 "409 conflicting change dedupe key").

// RegulatoryChangeService runs the change feed, impact assessment and review
// recommendations.
type RegulatoryChangeService struct {
	store         port.RegulatoryChangeRepository
	coverage      port.ContentCoverage
	registrations port.RegistrationRepository
	obligations   port.ObligationRepository
	tx            port.TxManager
	clock         clock.Clock
	ids           idgen.Generator
}

// NewRegulatoryChangeService wires the service.
func NewRegulatoryChangeService(store port.RegulatoryChangeRepository, coverage port.ContentCoverage,
	registrations port.RegistrationRepository, obligations port.ObligationRepository, tx port.TxManager,
	clk clock.Clock, ids idgen.Generator) *RegulatoryChangeService {
	return &RegulatoryChangeService{store: store, coverage: coverage, registrations: registrations, obligations: obligations,
		tx: tx, clock: clk, ids: ids}
}

var (
	regChangeReaders     = []security.Role{security.RoleOperator, security.RoleAnalyst, security.RoleAuditor}
	regChangeRecorders   = []security.Role{security.RoleOperator, security.RoleAnalyst}
	regChangeSpecialists = []security.Role{security.RoleAnalyst}
)

// regChangeListCap bounds what a listing, and an assessment, reads.
const regChangeListCap = 500

func (s *RegulatoryChangeService) now() time.Time {
	return s.clock.Now().UTC().Truncate(time.Microsecond)
}

func (s *RegulatoryChangeService) inTx(ctx context.Context, fn func(context.Context) error) error {
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

// BundleCoverage reads coverage from the cell's active content bundle.
type BundleCoverage struct{ Holder *rule.Holder }

// CoverageAt implements port.ContentCoverage.
func (b BundleCoverage) CoverageAt(t time.Time) (regchange.ContentSnapshot, error) {
	var bundle *rule.Bundle
	if b.Holder != nil {
		bundle = b.Holder.Current()
	}
	if bundle == nil || bundle.Taxation() == nil {
		return regchange.ContentSnapshot{}, errs.New(errs.CategoryUnavailable, errs.ReasonNoContentBundle,
			"The cell has no active content bundle with location-aware content, so no impact can be assessed against it. Nothing was recorded.")
	}
	out := regchange.ContentSnapshot{BundleID: bundle.ID(), BundleDigest: bundle.Digest(), At: t}
	for _, rg := range bundle.Taxation().Coverage(t) {
		for _, c := range rg.Components {
			out.Components = append(out.Components, regchange.ContentComponent{
				ID: c.ID, Regime: rg.ID, Jurisdiction: c.Jurisdiction, Authority: c.Authority, TaxType: c.TaxType,
				Name: c.Name, Services: append([]string(nil), c.Services...), From: c.From, To: c.To, Citation: c.Citation,
			})
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// changes
// ---------------------------------------------------------------------------

// ChangeView is a change with what the screen shows beside it.
type ChangeView struct {
	regchange.Change
	// Related are the other changes recorded from the same source edition.
	Related []id.RegulatoryChangeID
	// CurrentAssessment is the newest assessment, zero for none.
	CurrentAssessment id.ImpactAssessmentID
	CurrentSeverity   regchange.Severity
	// Recommendations counts the change's recommendations; Accepted those
	// accepted on the current assessment.
	Recommendations int
	Accepted        int
}

// ChangeInput is a change to record.
type ChangeInput struct {
	IntakeKey       string
	Jurisdiction    string
	Kind            regchange.Kind
	Title           string
	Summary         string
	Source          regchange.Source
	PublishedOn     time.Time
	EffectiveFrom   time.Time
	DiscoveredAt    time.Time // zero for now
	DetectedBy      regchange.Detector
	MonitorRef      string
	ServiceFamilies []string
	TaxTypes        []string
}

// RecordChange records a change as NEW. created is false when the
// Idempotency-Key already recorded this request, or the same source
// edition's change of this kind is already recorded as reported.
func (s *RegulatoryChangeService) RecordChange(ctx context.Context, in ChangeInput) (ChangeView, bool, error) {
	sc, err := requirePerson(ctx, regChangeRecorders...)
	if err != nil {
		return ChangeView{}, false, err
	}
	key := strings.TrimSpace(in.IntakeKey)
	if err := checkIntakeKey(key); err != nil {
		return ChangeView{}, false, err
	}
	now := s.now()
	c := regchange.Change{
		TenantID: sc.Tenant(), Jurisdiction: strings.ToUpper(strings.TrimSpace(in.Jurisdiction)), Kind: in.Kind,
		Title: strings.TrimSpace(in.Title), Summary: strings.TrimSpace(in.Summary), Source: in.Source.Normalize(),
		DetectedBy: in.DetectedBy, MonitorRef: strings.TrimSpace(in.MonitorRef),
		ServiceFamilies: regchange.NormalizeCodes(in.ServiceFamilies), TaxTypes: regchange.NormalizeCodes(in.TaxTypes),
		IntakeKey: key, CreatedBy: sc.Subject(), DiscoveredAt: in.DiscoveredAt.UTC().Truncate(time.Microsecond),
	}
	if !in.PublishedOn.IsZero() {
		c.PublishedOn = regchange.Day(in.PublishedOn)
	}
	if !in.EffectiveFrom.IsZero() {
		c.EffectiveFrom = regchange.Day(in.EffectiveFrom)
	}
	if in.DiscoveredAt.IsZero() {
		c.DiscoveredAt = now
	}
	if err := c.Validate(now); err != nil {
		return ChangeView{}, false, err
	}
	if c.SourceKey, c.DedupeKey, err = c.Keys(); err != nil {
		return ChangeView{}, false, internal(err, "The change could not be keyed.")
	}
	if c.IntakeHash, err = canonical.Sum(c.Payload()); err != nil {
		return ChangeView{}, false, internal(err, "The change could not be hashed.")
	}
	var (
		out     regchange.Change
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "intake/"+key); err != nil {
			return err
		}
		if prior, found, err := s.store.ChangeByIntakeKey(ctx, key); err != nil {
			return err
		} else if found {
			if !prior.IntakeHash.Equal(c.IntakeHash) {
				return keyReuse()
			}
			out = prior
			return nil
		}
		if err := s.store.Lock(ctx, "dedupe/"+c.DedupeKey.String()); err != nil {
			return err
		}
		if prior, found, err := s.store.ChangeByDedupeKey(ctx, c.DedupeKey); err != nil {
			return err
		} else if found {
			if !prior.IntakeHash.Equal(c.IntakeHash) {
				return errs.New(errs.CategoryConflict, regchange.ReasonDedupeConflict,
					fmt.Sprintf("Change %s already records this source edition's %s with different particulars.", prior.ID, c.Kind))
			}
			out = prior
			return nil
		}
		cid, err := idgen.RegulatoryChangeID(s.ids)
		if err != nil {
			return internal(err, "The change could not be recorded.")
		}
		c.ID, c.CreatedAt = cid, now
		events, err := regchange.Open(c, now)
		if err != nil {
			return err
		}
		if err := s.store.CreateChange(ctx, c, events); err != nil {
			return err
		}
		created = true
		out, err = s.store.Change(ctx, cid)
		return err
	})
	if err != nil {
		return ChangeView{}, false, err
	}
	v, err := s.changeView(ctx, out, true)
	return v, created, err
}

func (s *RegulatoryChangeService) changeView(ctx context.Context, c regchange.Change, detail bool) (ChangeView, error) {
	v := ChangeView{Change: c, Related: []id.RegulatoryChangeID{}}
	if !detail {
		return v, nil
	}
	same, err := s.store.ChangesBySource(ctx, c.SourceKey)
	if err != nil {
		return ChangeView{}, err
	}
	for _, o := range same {
		if o.ID != c.ID {
			v.Related = append(v.Related, o.ID)
		}
	}
	cl, current, err := s.closing(ctx, c.ID)
	if err != nil {
		return ChangeView{}, err
	}
	if cl.HasAssessment {
		v.CurrentAssessment, v.CurrentSeverity = current.ID, current.Severity
	}
	recs, err := s.store.RecommendationsOf(ctx, c.ID)
	if err != nil {
		return ChangeView{}, err
	}
	v.Recommendations = len(recs)
	for _, r := range recs {
		if r.Status == regchange.RecAccepted && r.Assessment == current.ID {
			v.Accepted++
		}
	}
	return v, nil
}

// closing is what closure needs: the current assessment, and whether a
// recommendation on it has been accepted.
func (s *RegulatoryChangeService) closing(ctx context.Context, changeID id.RegulatoryChangeID) (regchange.Closing, regchange.Assessment, error) {
	all, err := s.store.AssessmentsOf(ctx, changeID)
	if err != nil || len(all) == 0 {
		return regchange.Closing{}, regchange.Assessment{}, err
	}
	current := all[0]
	recs, err := s.store.RecommendationsOf(ctx, changeID)
	if err != nil {
		return regchange.Closing{}, regchange.Assessment{}, err
	}
	cl := regchange.Closing{HasAssessment: true}
	for _, r := range recs {
		if r.Status == regchange.RecAccepted && r.Assessment == current.ID {
			cl.HasAcceptedRecommendation = true
		}
	}
	return cl, current, nil
}

// Changes lists changes, newest first.
func (s *RegulatoryChangeService) Changes(ctx context.Context, f regchange.Filter, limit int) ([]ChangeView, error) {
	if _, err := requireRoleOrSystem(ctx, regChangeReaders...); err != nil {
		return nil, err
	}
	all, err := s.store.Changes(ctx, regChangeListCap)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > regChangeListCap {
		limit = regChangeListCap
	}
	out := []ChangeView{}
	for _, c := range all {
		if f.Match(c) && len(out) < limit {
			v, err := s.changeView(ctx, c, false)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
	}
	return out, nil
}

// Change reads one change.
func (s *RegulatoryChangeService) Change(ctx context.Context, changeID id.RegulatoryChangeID) (ChangeView, error) {
	if _, err := requireRoleOrSystem(ctx, regChangeReaders...); err != nil {
		return ChangeView{}, err
	}
	c, err := s.store.Change(ctx, changeID)
	if err != nil {
		return ChangeView{}, err
	}
	return s.changeView(ctx, c, true)
}

// TransitionChange records a specialist's triage, hold, resumption or
// decision on a change, against the version they read.
func (s *RegulatoryChangeService) TransitionChange(ctx context.Context, changeID id.RegulatoryChangeID, a regchange.Act) (ChangeView, error) {
	sc, err := requirePerson(ctx, regChangeSpecialists...)
	if err != nil {
		return ChangeView{}, err
	}
	var out regchange.Change
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "change/"+changeID.String()); err != nil {
			return err
		}
		c, err := s.store.Change(ctx, changeID)
		if err != nil {
			return err
		}
		cl, _, err := s.closing(ctx, changeID)
		if err != nil {
			return err
		}
		e, err := c.Transition(a, cl, sc.Subject(), s.now())
		if err != nil {
			return err
		}
		if err := s.store.AppendChangeEvents(ctx, changeID, []regchange.Event{e}); err != nil {
			return err
		}
		out, err = s.store.Change(ctx, changeID)
		return err
	})
	if err != nil {
		return ChangeView{}, err
	}
	return s.changeView(ctx, out, true)
}

// ---------------------------------------------------------------------------
// impact assessments
// ---------------------------------------------------------------------------

// AssessChange assesses a triaged change's impact against the active
// content at its effective date and the tenant's registrations and
// obligations. Assessing again on the same inputs records nothing and
// returns the current assessment (created false); on changed inputs it
// records a new current assessment, and recommendations on the old one can
// no longer be recommended or reviewed. The first assessment moves the
// change to IN_IMPACT_ASSESSMENT.
func (s *RegulatoryChangeService) AssessChange(ctx context.Context, changeID id.RegulatoryChangeID) (regchange.Assessment, bool, error) {
	sc, err := requirePerson(ctx, regChangeSpecialists...)
	if err != nil {
		return regchange.Assessment{}, false, err
	}
	var (
		out     regchange.Assessment
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "change/"+changeID.String()); err != nil {
			return err
		}
		c, err := s.store.Change(ctx, changeID)
		if err != nil {
			return err
		}
		now := s.now()
		if _, _, err := c.StartAssessment(id.ImpactAssessmentID{}, sc.Subject(), now); err != nil {
			return err
		}
		in, err := s.inputs(ctx, c)
		if err != nil {
			return err
		}
		res, err := regchange.Assess(in)
		if err != nil {
			return internal(err, "The assessment could not be digested.")
		}
		prior, err := s.store.AssessmentsOf(ctx, changeID)
		if err != nil {
			return err
		}
		if len(prior) > 0 && prior[0].InputsDigest.Equal(res.InputsDigest) {
			out = prior[0]
			return nil
		}
		aid, err := idgen.ImpactAssessmentID(s.ids)
		if err != nil {
			return internal(err, "The assessment could not be recorded.")
		}
		out = regchange.Assessment{
			ID: aid, TenantID: sc.Tenant(), Change: changeID, BundleID: in.Content.BundleID,
			BundleDigest: in.Content.BundleDigest, AsOf: in.Content.At, Result: res,
			ComponentsRead: len(in.Content.Components), RegistrationsRead: len(in.Registrations),
			ObligationsRead: len(in.Obligations), CreatedAt: now, CreatedBy: sc.Subject(),
		}
		if err := s.store.CreateAssessment(ctx, out); err != nil {
			return err
		}
		created = true
		e, started, err := c.StartAssessment(aid, sc.Subject(), now)
		if err != nil {
			return err
		}
		if started {
			return s.store.AppendChangeEvents(ctx, changeID, []regchange.Event{e})
		}
		return nil
	})
	if err != nil {
		return regchange.Assessment{}, false, err
	}
	return out, created, nil
}

// inputs gathers what an assessment reads. Each read is bounded; the counts
// recorded on the assessment say how much was read.
func (s *RegulatoryChangeService) inputs(ctx context.Context, c regchange.Change) (regchange.Inputs, error) {
	snap, err := s.coverage.CoverageAt(c.EffectiveFrom)
	if err != nil {
		return regchange.Inputs{}, err
	}
	in := regchange.Inputs{Change: c, Content: snap}
	regs, err := s.registrations.Registrations(ctx, regChangeListCap)
	if err != nil {
		return regchange.Inputs{}, err
	}
	for _, r := range regs {
		in.Registrations = append(in.Registrations, regchange.RegistrationRef{
			ID: r.ID.String(), Scheme: string(r.Scheme), Jurisdiction: r.Jurisdiction, Authority: r.Authority,
			Status: string(r.Status), Version: r.Version(),
		})
	}
	obls, err := s.obligations.List(ctx, port.ObligationFilter{Limit: regChangeListCap})
	if err != nil {
		return regchange.Inputs{}, err
	}
	for _, o := range obls {
		in.Obligations = append(in.Obligations, regchange.ObligationRef{
			ID: o.ID.String(), BusinessKey: o.BusinessKey, Jurisdiction: o.JurisdictionID, Type: o.Type,
			Status: string(o.Status), PeriodStart: o.Period.Start, PeriodEnd: o.Period.End,
		})
	}
	return in, nil
}

// AssessmentView is an assessment and whether it is its change's current
// one.
type AssessmentView struct {
	regchange.Assessment
	Current bool
}

// Assessment reads one assessment, verified against its digest.
func (s *RegulatoryChangeService) Assessment(ctx context.Context, assessmentID id.ImpactAssessmentID) (AssessmentView, error) {
	if _, err := requireRoleOrSystem(ctx, regChangeReaders...); err != nil {
		return AssessmentView{}, err
	}
	a, err := s.store.Assessment(ctx, assessmentID)
	if err != nil {
		return AssessmentView{}, err
	}
	if err := a.Verify(); err != nil {
		return AssessmentView{}, errs.Wrap(err, errs.CategoryInternal, errs.ReasonEvidenceIntegrity,
			"A stored impact assessment does not match its digest. It is not shown.")
	}
	all, err := s.store.AssessmentsOf(ctx, a.Change)
	if err != nil {
		return AssessmentView{}, err
	}
	return AssessmentView{Assessment: a, Current: len(all) > 0 && all[0].ID == a.ID}, nil
}

// Assessments lists assessments, newest first: one change's, or the
// tenant's, optionally of at least a severity.
func (s *RegulatoryChangeService) Assessments(ctx context.Context, changeID id.RegulatoryChangeID, minSeverity regchange.Severity,
	limit int) ([]AssessmentView, error) {
	if _, err := requireRoleOrSystem(ctx, regChangeReaders...); err != nil {
		return nil, err
	}
	var (
		all []regchange.Assessment
		err error
	)
	if changeID.IsZero() {
		all, err = s.store.Assessments(ctx, regChangeListCap)
	} else {
		all, err = s.store.AssessmentsOf(ctx, changeID)
	}
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > regChangeListCap {
		limit = regChangeListCap
	}
	latest := map[id.RegulatoryChangeID]id.ImpactAssessmentID{}
	out := []AssessmentView{}
	for _, a := range all {
		if _, seen := latest[a.Change]; !seen {
			// The store returns a change's assessments newest first, and
			// the tenant's newest first, so the first seen is current.
			latest[a.Change] = a.ID
		}
		if minSeverity != "" && severityRank(a.Severity) < severityRank(minSeverity) {
			continue
		}
		if len(out) < limit {
			out = append(out, AssessmentView{Assessment: a, Current: latest[a.Change] == a.ID})
		}
	}
	return out, nil
}

func severityRank(s regchange.Severity) int {
	for i, v := range regchange.Severities {
		if v == s {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// review recommendations
// ---------------------------------------------------------------------------

// RecommendationView is a recommendation with its change's current
// assessment, so the screen can say when it was overtaken.
type RecommendationView struct {
	regchange.Recommendation
	CurrentAssessment id.ImpactAssessmentID
}

// Stale reports whether the change has been reassessed since.
func (v RecommendationView) Stale() bool {
	return !v.Status.Final() && v.CurrentAssessment != v.Assessment
}

// RecommendationInput is a recommendation to draft.
type RecommendationInput struct {
	IntakeKey  string
	Assessment id.ImpactAssessmentID
	Origin     regchange.Origin
	AI         *regchange.AIProvenance
	Content    regchange.Content
}

// DraftRecommendation drafts a recommendation on a change's current
// assessment. created is false when the Idempotency-Key already drafted
// this request.
func (s *RegulatoryChangeService) DraftRecommendation(ctx context.Context, in RecommendationInput) (RecommendationView, bool, error) {
	sc, err := requirePerson(ctx, regChangeSpecialists...)
	if err != nil {
		return RecommendationView{}, false, err
	}
	key := strings.TrimSpace(in.IntakeKey)
	if err := checkIntakeKey(key); err != nil {
		return RecommendationView{}, false, err
	}
	if in.AI != nil {
		p := *in.AI
		p.UseCase, p.ModelProfile, p.ProviderProfile = strings.TrimSpace(p.UseCase), strings.TrimSpace(p.ModelProfile), strings.TrimSpace(p.ProviderProfile)
		p.PromptProfile, p.AiTrainVersion, p.InvocationID = strings.TrimSpace(p.PromptProfile), strings.TrimSpace(p.AiTrainVersion), strings.TrimSpace(p.InvocationID)
		in.AI = &p
	}
	content := in.Content.Normalize()
	var (
		out     regchange.Recommendation
		current id.ImpactAssessmentID
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		a, err := s.store.Assessment(ctx, in.Assessment)
		if err != nil {
			if errs.IsCategory(err, errs.CategoryNotFound) {
				return errs.Invalid("assessmentId", errs.ReasonInvalidValue, "That is not one of the tenant's impact assessments.")
			}
			return err
		}
		if err := s.store.Lock(ctx, "change/"+a.Change.String()); err != nil {
			return err
		}
		r := regchange.Recommendation{TenantID: sc.Tenant(), Change: a.Change, Assessment: a.ID, AssessmentDigest: a.AssessmentDigest,
			Origin: in.Origin, AI: in.AI, IntakeKey: key, CreatedBy: sc.Subject()}
		if r.IntakeHash, err = canonical.Sum(r.Payload(content)); err != nil {
			return internal(err, "The recommendation could not be hashed.")
		}
		if err := s.store.Lock(ctx, "intake/"+key); err != nil {
			return err
		}
		if prior, found, err := s.store.RecommendationByIntakeKey(ctx, key); err != nil {
			return err
		} else if found {
			if !prior.IntakeHash.Equal(r.IntakeHash) {
				return keyReuse()
			}
			out = prior
			_, cur, err := s.closing(ctx, prior.Change)
			current = cur.ID
			return err
		}
		c, err := s.store.Change(ctx, a.Change)
		if err != nil {
			return err
		}
		if c.Status != regchange.StatusInAssessment {
			return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				fmt.Sprintf("Recommendations are drafted on a change IN_IMPACT_ASSESSMENT; this change is %s.", c.Status))
		}
		_, cur, err := s.closing(ctx, a.Change)
		if err != nil {
			return err
		}
		if cur.ID != a.ID {
			return errs.New(errs.CategoryConflict, regchange.ReasonAssessmentSuperseded,
				fmt.Sprintf("Assessment %s is not the change's current assessment; %s is.", a.ID, cur.ID))
		}
		current = cur.ID
		rid, err := idgen.ReviewRecommendationID(s.ids)
		if err != nil {
			return internal(err, "The recommendation could not be recorded.")
		}
		now := s.now()
		r.ID, r.CreatedAt = rid, now
		e, err := regchange.Draft(r, content, a, sc.Subject(), now)
		if err != nil {
			return err
		}
		if err := s.store.CreateRecommendation(ctx, r, e); err != nil {
			return err
		}
		created = true
		out, err = s.store.Recommendation(ctx, rid)
		return err
	})
	if err != nil {
		return RecommendationView{}, false, err
	}
	return RecommendationView{Recommendation: out, CurrentAssessment: current}, created, nil
}

// Recommendations lists recommendations, newest first.
func (s *RegulatoryChangeService) Recommendations(ctx context.Context, f regchange.RecFilter, limit int) ([]RecommendationView, error) {
	if _, err := requireRoleOrSystem(ctx, regChangeReaders...); err != nil {
		return nil, err
	}
	var (
		all []regchange.Recommendation
		err error
	)
	if f.Change.IsZero() {
		all, err = s.store.Recommendations(ctx, regChangeListCap)
	} else {
		all, err = s.store.RecommendationsOf(ctx, f.Change)
	}
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > regChangeListCap {
		limit = regChangeListCap
	}
	currents := map[id.RegulatoryChangeID]id.ImpactAssessmentID{}
	out := []RecommendationView{}
	for _, r := range all {
		if !f.Match(r) || len(out) >= limit {
			continue
		}
		cur, ok := currents[r.Change]
		if !ok {
			_, a, err := s.closing(ctx, r.Change)
			if err != nil {
				return nil, err
			}
			cur, currents[r.Change] = a.ID, a.ID
		}
		out = append(out, RecommendationView{Recommendation: r, CurrentAssessment: cur})
	}
	return out, nil
}

// Recommendation reads one recommendation.
func (s *RegulatoryChangeService) Recommendation(ctx context.Context, recommendationID id.ReviewRecommendationID) (RecommendationView, error) {
	if _, err := requireRoleOrSystem(ctx, regChangeReaders...); err != nil {
		return RecommendationView{}, err
	}
	r, err := s.store.Recommendation(ctx, recommendationID)
	if err != nil {
		return RecommendationView{}, err
	}
	_, a, err := s.closing(ctx, r.Change)
	if err != nil {
		return RecommendationView{}, err
	}
	return RecommendationView{Recommendation: r, CurrentAssessment: a.ID}, nil
}

// ReviseRecommendation replaces what a DRAFT or CHANGES_REQUESTED
// recommendation says.
func (s *RegulatoryChangeService) ReviseRecommendation(ctx context.Context, recommendationID id.ReviewRecommendationID,
	c regchange.Content, version int) (RecommendationView, error) {
	return s.actRecommendation(ctx, recommendationID, func(r regchange.Recommendation, current regchange.Assessment, by id.UserID, at time.Time) (regchange.RecEvent, error) {
		if current.ID != r.Assessment {
			return regchange.RecEvent{}, errs.New(errs.CategoryConflict, regchange.ReasonAssessmentSuperseded,
				"The change has been reassessed since this recommendation was drafted. Withdraw it and draft one on the current assessment.")
		}
		return r.Revise(c, current, version, by, at)
	})
}

// RecommendRecommendation is a specialist's recommendation of a draft.
func (s *RegulatoryChangeService) RecommendRecommendation(ctx context.Context, recommendationID id.ReviewRecommendationID,
	reason string, version int) (RecommendationView, error) {
	return s.actRecommendation(ctx, recommendationID, func(r regchange.Recommendation, current regchange.Assessment, by id.UserID, at time.Time) (regchange.RecEvent, error) {
		return r.Recommend(reason, version, current.ID, by, at)
	})
}

// ReviewRecommendation is the independent review of a recommended
// recommendation. ACCEPT records a hand-off reference and nothing else.
func (s *RegulatoryChangeService) ReviewRecommendation(ctx context.Context, recommendationID id.ReviewRecommendationID,
	d regchange.Decision, reason string, version int) (RecommendationView, error) {
	return s.actRecommendation(ctx, recommendationID, func(r regchange.Recommendation, current regchange.Assessment, by id.UserID, at time.Time) (regchange.RecEvent, error) {
		return r.Decide(d, reason, version, current.ID, by, at)
	})
}

// WithdrawRecommendation withdraws an open recommendation.
func (s *RegulatoryChangeService) WithdrawRecommendation(ctx context.Context, recommendationID id.ReviewRecommendationID,
	reason string, version int) (RecommendationView, error) {
	return s.actRecommendation(ctx, recommendationID, func(r regchange.Recommendation, _ regchange.Assessment, by id.UserID, at time.Time) (regchange.RecEvent, error) {
		return r.Withdraw(reason, version, by, at)
	})
}

func (s *RegulatoryChangeService) actRecommendation(ctx context.Context, recommendationID id.ReviewRecommendationID,
	fn func(regchange.Recommendation, regchange.Assessment, id.UserID, time.Time) (regchange.RecEvent, error)) (RecommendationView, error) {
	sc, err := requirePerson(ctx, regChangeSpecialists...)
	if err != nil {
		return RecommendationView{}, err
	}
	var (
		out     regchange.Recommendation
		current regchange.Assessment
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		r, err := s.store.Recommendation(ctx, recommendationID)
		if err != nil {
			return err
		}
		// The change's lock serializes this act against a reassessment, so
		// "current" is compared against the assessment that is current.
		if err := s.store.Lock(ctx, "change/"+r.Change.String()); err != nil {
			return err
		}
		if r, err = s.store.Recommendation(ctx, recommendationID); err != nil {
			return err
		}
		if _, current, err = s.closing(ctx, r.Change); err != nil {
			return err
		}
		e, err := fn(r, current, sc.Subject(), s.now())
		if err != nil {
			return err
		}
		if err := s.store.AppendRecommendationEvent(ctx, recommendationID, e); err != nil {
			return err
		}
		out, err = s.store.Recommendation(ctx, recommendationID)
		return err
	})
	if err != nil {
		return RecommendationView{}, err
	}
	return RecommendationView{Recommendation: out, CurrentAssessment: current.ID}, nil
}
