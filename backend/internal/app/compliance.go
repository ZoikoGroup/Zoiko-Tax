package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/compliance"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/legal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Compliance operations (ZTAX-COMP-001, W4 lane M). The rules of a package
// are internal/domain/compliance's; this is where they meet the obligation
// they execute, the legal gate a submission passes, and the store.
//
// Every write is a person's act. A compliance step records who did it, and
// segregation of duties is a comparison of those people — so system work and
// models cannot prepare, review, approve or submit (ZTAX-COMP-REQ-0042).
//
// The obligation's status moves only on evidence the package records: an
// acknowledgement or a resolution. Starting a submission moves nothing,
// because TRANSMITTING is not FILED (ZTAX-COMP-REQ-0055, -0058).

// ComplianceService runs compliance packages.
type ComplianceService struct {
	packages    port.ComplianceRepository
	obligations port.ObligationRepository
	filing      *DeterminationService
	legal       *LegalService
	tx          port.TxManager
	clock       clock.Clock
	ids         idgen.Generator
}

// NewComplianceService wires the service. filing moves obligations on
// filing evidence; legal gates submissions, and nil blocks every one.
func NewComplianceService(packages port.ComplianceRepository, obligations port.ObligationRepository, filing *DeterminationService,
	legalSvc *LegalService, tx port.TxManager, clk clock.Clock, ids idgen.Generator) *ComplianceService {
	return &ComplianceService{packages: packages, obligations: obligations, filing: filing, legal: legalSvc, tx: tx, clock: clk, ids: ids}
}

func (s *ComplianceService) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }

// The roles. Reading is open to the fiscal roles; preparing, submitting and
// recording the authority's answer are the operator's; reviewing and
// approving may be an analyst's, so a second person is available to do it.
var (
	complianceReaders   = []security.Role{security.RoleOperator, security.RoleAnalyst, security.RoleAuditor}
	complianceOperators = []security.Role{security.RoleOperator}
	complianceReviewers = []security.Role{security.RoleOperator, security.RoleAnalyst}
)

// PackageReadiness is one dimension of whether a package can proceed
// (ZTAX-COMP-001 §8).
type PackageReadiness struct {
	Dimension string
	Ready     bool
	Detail    string
}

// PackageView is a package as of now: its obligation's current status, its
// readiness, its overdue flags and its queue priority.
type PackageView struct {
	compliance.Package
	ObligationStatus obligation.Status
	Readiness        []PackageReadiness
	// Ready is every readiness dimension passing. Unknown is not ready
	// (ZTAX-COMP-REQ-0025).
	Ready    bool
	Overdue  compliance.Overdue
	Priority compliance.Priority
	// Gate is the legal gate's answer for filing this package now.
	Gate legal.Resolution
}

// CreatePackageInput is a package to instantiate.
type CreatePackageInput struct {
	Obligation      id.ObligationID
	Mode            compliance.Mode
	Channel         compliance.Channel
	FilingCountry   string
	FilingAuthority string
	PrepareDue      time.Time
	ReviewDue       time.Time
	SubmitTarget    time.Time
}

// CreatePackage instantiates the package for an obligation. It is idempotent
// for the obligation: asking again returns the package already made, and
// reports that it did (ZTAX-COMP-REQ-0004).
func (s *ComplianceService) CreatePackage(ctx context.Context, in CreatePackageInput) (PackageView, bool, error) {
	sc, err := requirePerson(ctx, complianceOperators...)
	if err != nil {
		return PackageView{}, false, err
	}
	var (
		out     compliance.Package
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		o, err := s.obligations.ByID(ctx, in.Obligation)
		if err != nil {
			return err
		}
		if err := s.packages.Lock(ctx, o.BusinessKey); err != nil {
			return err
		}
		existing, err := s.packages.ByObligation(ctx, o.BusinessKey)
		if err == nil {
			out = existing
			return nil
		}
		if !errs.IsCategory(err, errs.CategoryNotFound) {
			return err
		}
		cur, err := s.obligations.Current(ctx, o.BusinessKey)
		if err != nil {
			return err
		}
		if cur.Status == obligation.StatusClosed {
			return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				"The obligation is closed; there is nothing left to file for it.")
		}
		pid, err := idgen.CompliancePackageID(s.ids)
		if err != nil {
			return internal(err, "The compliance package could not be created.")
		}
		loc := cur.Period.Due.Location()
		due := cur.Period.Due
		if !cur.Due.Legal.IsZero() {
			due = cur.Due.Legal
		}
		now := s.now()
		p := compliance.Package{
			ID: pid, TenantID: sc.Tenant(), Obligation: cur.ID, ObligationKey: cur.BusinessKey,
			Responsibility: cur.Responsibility, LegalEntity: cur.LegalEntity, ObligationType: cur.Type,
			Jurisdiction: cur.JurisdictionID, PeriodStart: cur.Period.Start, PeriodEnd: cur.Period.End,
			Mode: in.Mode, Channel: in.Channel, FilingCountry: strings.ToUpper(strings.TrimSpace(in.FilingCountry)),
			FilingAuthority: strings.TrimSpace(in.FilingAuthority),
			// The legal due date is a civil date in the authority's calendar;
			// the deadline is the end of that day there (ZTAX-COMP-REQ-0009).
			StatutoryDue: time.Date(due.Year(), due.Month(), due.Day(), 23, 59, 59, 999999000, loc),
			Timezone:     loc.String(),
			PrepareDue:   in.PrepareDue, ReviewDue: in.ReviewDue, SubmitTarget: in.SubmitTarget,
			CreatedAt: now, CreatedBy: sc.Subject(),
		}
		if p.LegalEntity.IsZero() {
			return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				"The obligation names no legal entity, so there is no one to file for.")
		}
		if err := p.Validate(); err != nil {
			return errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue, strings.TrimPrefix(err.Error(), "compliance: ")+".")
		}
		first := compliance.Step{Seq: 1, Kind: compliance.StepCreated, Status: compliance.StatusReady, RecordedAt: now, RecordedBy: sc.Subject()}
		if err := s.packages.Create(ctx, p, first); err != nil {
			return err
		}
		created = true
		out, err = s.packages.Package(ctx, pid)
		return err
	})
	if err != nil {
		return PackageView{}, false, err
	}
	v, err := s.view(ctx, out)
	return v, created, err
}

// Package reads one package as of now.
func (s *ComplianceService) Package(ctx context.Context, packageID id.CompliancePackageID) (PackageView, error) {
	if _, err := requireRoleOrSystem(ctx, complianceReaders...); err != nil {
		return PackageView{}, err
	}
	p, err := s.packages.Package(ctx, packageID)
	if err != nil {
		return PackageView{}, err
	}
	return s.view(ctx, p)
}

// Packages lists the tenant's packages in queue order: priority first, then
// the statutory deadline (ZTAX-COMP-REQ-0091). An empty status is all.
func (s *ComplianceService) Packages(ctx context.Context, status compliance.Status, limit int) ([]PackageView, error) {
	if _, err := requireRoleOrSystem(ctx, complianceReaders...); err != nil {
		return nil, err
	}
	rows, err := s.packages.List(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]PackageView, 0, len(rows))
	for _, p := range rows {
		if status != "" && p.Status != status {
			continue
		}
		v, err := s.view(ctx, p)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	// Stable: List is by statutory deadline already.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Priority < out[j-1].Priority; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// Prepare records a new preparation version of the obligation as it stands:
// its assessment is the control total, and the payload hash covers what the
// package files for and that figure (ZTAX-COMP-REQ-0035, -0036, -0039). The
// obligation must be ready — its data complete — or rejected and being
// remediated.
func (s *ComplianceService) Prepare(ctx context.Context, packageID id.CompliancePackageID, note string) (PackageView, error) {
	sc, err := requirePerson(ctx, complianceOperators...)
	if err != nil {
		return PackageView{}, err
	}
	return s.act(ctx, packageID, func(ctx context.Context, p compliance.Package) (compliance.Step, error) {
		cur, err := s.obligations.Current(ctx, p.ObligationKey)
		if err != nil {
			return compliance.Step{}, err
		}
		if cur.Status != obligation.StatusReady && cur.Status != obligation.StatusRejected {
			return compliance.Step{}, errs.New(errs.CategoryConflict, compliance.ReasonFilingBlocked,
				fmt.Sprintf("The obligation is %s. It is prepared once its data is complete and it is marked READY (ZTAX-COMP-REQ-0018).", cur.Status))
		}
		sums, err := s.obligations.Assessed(ctx, []string{p.ObligationKey})
		if err != nil {
			return compliance.Step{}, err
		}
		version := 1
		if prev := p.Preparation(); prev != nil {
			version = prev.Version + 1
		}
		total := canonical.Absent()
		var control = cur.Assessed
		if t, ok := sums[p.ObligationKey]; ok {
			control = &t
		}
		if control != nil {
			total = canonical.Money(*control)
		}
		currency := canonical.Absent()
		if control != nil {
			currency = canonical.String(string(control.Currency()))
		}
		hash, err := canonical.Sum(canonical.Object(
			canonical.F("package", p.Canonical()),
			canonical.F("obligationRow", canonical.String(cur.ID.String())),
			canonical.F("form", canonical.String(cur.Definition.ID+"@"+cur.Definition.Version)),
			canonical.F("controlTotal", total),
			canonical.F("currency", currency),
			canonical.F("version", canonical.Integer(int64(version))),
		))
		if err != nil {
			return compliance.Step{}, internal(err, "The preparation could not be hashed.")
		}
		return p.Prepare(hash, control, strings.TrimSpace(note), sc.Subject(), s.now())
	})
}

// Review records a reviewer's decision on the version they name.
func (s *ComplianceService) Review(ctx context.Context, packageID id.CompliancePackageID, version int,
	decision compliance.ReviewDecision, note string) (PackageView, error) {
	sc, err := requirePerson(ctx, complianceReviewers...)
	if err != nil {
		return PackageView{}, err
	}
	return s.act(ctx, packageID, func(_ context.Context, p compliance.Package) (compliance.Step, error) {
		return p.Review(version, decision, strings.TrimSpace(note), sc.Subject(), s.now())
	})
}

// Approve pins the exact version and payload hash that may be submitted.
func (s *ComplianceService) Approve(ctx context.Context, packageID id.CompliancePackageID, version int,
	payloadHash canonical.Digest, note string) (PackageView, error) {
	sc, err := requirePerson(ctx, complianceReviewers...)
	if err != nil {
		return PackageView{}, err
	}
	return s.act(ctx, packageID, func(_ context.Context, p compliance.Package) (compliance.Step, error) {
		return p.Approve(version, payloadHash, strings.TrimSpace(note), sc.Subject(), s.now())
	})
}

// Submit starts a submission attempt after pre-submission validation: the
// obligation still holds the figure that was approved, and the legal gate
// allows filing for this authority, legal entity and period now
// (ZTAX-COMP-REQ-0046 to -0049, -0071, -0073).
func (s *ComplianceService) Submit(ctx context.Context, packageID id.CompliancePackageID, note string) (PackageView, error) {
	sc, err := requirePerson(ctx, complianceOperators...)
	if err != nil {
		return PackageView{}, err
	}
	return s.act(ctx, packageID, func(ctx context.Context, p compliance.Package) (compliance.Step, error) {
		// The package's own state first: an unapproved package is refused as
		// out of order before anything is asked of the gate.
		attempt, err := idgen.SubmissionAttemptID(s.ids)
		if err != nil {
			return compliance.Step{}, internal(err, "The submission attempt could not be recorded.")
		}
		step, err := p.Submit(attempt, strings.TrimSpace(note), sc.Subject(), s.now())
		if err != nil {
			return compliance.Step{}, err
		}
		cur, err := s.obligations.Current(ctx, p.ObligationKey)
		if err != nil {
			return compliance.Step{}, err
		}
		if !filingStatuses[cur.Status] || cur.Status == obligation.StatusFiled || cur.Status == obligation.StatusAccepted {
			return compliance.Step{}, errs.New(errs.CategoryConflict, compliance.ReasonFilingBlocked,
				fmt.Sprintf("The obligation is %s; it is not awaiting a filing.", cur.Status))
		}
		sums, err := s.obligations.Assessed(ctx, []string{p.ObligationKey})
		if err != nil {
			return compliance.Step{}, err
		}
		if t, ok := sums[p.ObligationKey]; ok && !sameMoney(p.Preparation().ControlTotal, t) {
			return compliance.Step{}, errs.New(errs.CategoryConflict, compliance.ReasonFilingBlocked,
				"The obligation's assessment has changed since this version was prepared; prepare it again (ZTAX-COMP-REQ-0045).")
		}
		res, _, err := s.gate(ctx, p)
		if err != nil {
			return compliance.Step{}, err
		}
		if !res.Allowed {
			return compliance.Step{}, errs.New(errs.CategoryPolicy, compliance.ReasonFilingBlocked,
				fmt.Sprintf("The legal gate blocks filing with %s %s: %s (%s).", p.FilingCountry, p.FilingAuthority, res.Detail, res.Reason))
		}
		return step, nil
	})
}

// AcknowledgeInput is what the authority answered.
type AcknowledgeInput struct {
	Acknowledgement compliance.Acknowledgement
	AuthorityRef    string
	Evidence        string
	Note            string
}

// Acknowledge records the authority's answer to the latest attempt, and
// moves the obligation to what that answer establishes.
func (s *ComplianceService) Acknowledge(ctx context.Context, packageID id.CompliancePackageID, in AcknowledgeInput) (PackageView, error) {
	sc, err := requirePerson(ctx, complianceOperators...)
	if err != nil {
		return PackageView{}, err
	}
	return s.act(ctx, packageID, func(ctx context.Context, p compliance.Package) (compliance.Step, error) {
		step, err := p.Acknowledge(in.Acknowledgement, in.AuthorityRef, in.Evidence, strings.TrimSpace(in.Note), sc.Subject(), s.now())
		if err != nil {
			return compliance.Step{}, err
		}
		return step, s.follow(ctx, p, step.Status, sc.Subject())
	})
}

// Resolve settles an uncertain submission with what was established at the
// authority.
func (s *ComplianceService) Resolve(ctx context.Context, packageID id.CompliancePackageID, res compliance.Resolution,
	authorityRef, evidence string) (PackageView, error) {
	sc, err := requirePerson(ctx, complianceOperators...)
	if err != nil {
		return PackageView{}, err
	}
	return s.act(ctx, packageID, func(ctx context.Context, p compliance.Package) (compliance.Step, error) {
		step, err := p.Resolve(res, authorityRef, evidence, sc.Subject(), s.now())
		if err != nil {
			return compliance.Step{}, err
		}
		if step.Status == compliance.StatusFiled {
			return step, s.follow(ctx, p, step.Status, sc.Subject())
		}
		return step, nil
	})
}

// Cancel closes a package that has not been submitted.
func (s *ComplianceService) Cancel(ctx context.Context, packageID id.CompliancePackageID, reason string) (PackageView, error) {
	sc, err := requirePerson(ctx, complianceOperators...)
	if err != nil {
		return PackageView{}, err
	}
	return s.act(ctx, packageID, func(_ context.Context, p compliance.Package) (compliance.Step, error) {
		return p.Cancel(strings.TrimSpace(reason), sc.Subject(), s.now())
	})
}

// follow moves the obligation to the status a package's filing evidence
// establishes.
func (s *ComplianceService) follow(ctx context.Context, p compliance.Package, to compliance.Status, by id.UserID) error {
	target := map[compliance.Status]obligation.Status{
		compliance.StatusFiled:     obligation.StatusFiled,
		compliance.StatusAccepted:  obligation.StatusAccepted,
		compliance.StatusRejected:  obligation.StatusRejected,
		compliance.StatusUncertain: obligation.StatusUncertain,
	}[to]
	if target == "" {
		return nil
	}
	if s.filing == nil {
		return errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable, "Obligations are not wired in this cell.")
	}
	return s.filing.advanceFiling(ctx, p.ObligationKey, target, by)
}

// act applies one step to a package under its obligation's lock, and returns
// the package as of now.
func (s *ComplianceService) act(ctx context.Context, packageID id.CompliancePackageID,
	fn func(context.Context, compliance.Package) (compliance.Step, error)) (PackageView, error) {
	var out compliance.Package
	err := s.inTx(ctx, func(ctx context.Context) error {
		p, err := s.packages.Package(ctx, packageID)
		if err != nil {
			return err
		}
		if err := s.packages.Lock(ctx, p.ObligationKey); err != nil {
			return err
		}
		// Read again under the lock: a step decided against a history
		// somebody has since extended is decided against the wrong one.
		if p, err = s.packages.Package(ctx, packageID); err != nil {
			return err
		}
		step, err := fn(ctx, p)
		if err != nil {
			return err
		}
		if err := s.packages.AppendStep(ctx, p.ID, step); err != nil {
			return err
		}
		out, err = s.packages.Package(ctx, packageID)
		return err
	})
	if err != nil {
		return PackageView{}, err
	}
	return s.view(ctx, out)
}

// gate asks the legal gate whether this package may be filed now.
func (s *ComplianceService) gate(ctx context.Context, p compliance.Package) (legal.Resolution, time.Time, error) {
	if s.legal == nil {
		return legal.Resolution{Reason: legal.ReasonUnsupportedMatrix,
			Detail: "Legal authorization is not wired in this cell; every filing is blocked."}, s.now(), nil
	}
	return s.legal.resolve(ctx, legal.Action{
		Country: p.FilingCountry, Authority: p.FilingAuthority, Service: legal.ServiceFile,
		LegalEntity: p.LegalEntity, Period: p.PeriodStart.Format("2006-01"), Matter: p.ObligationType,
	})
}

// view attaches what a package's state means now.
func (s *ComplianceService) view(ctx context.Context, p compliance.Package) (PackageView, error) {
	now := s.now()
	v := PackageView{Package: p, Overdue: p.OverdueAt(now), Priority: p.PriorityAt(now)}
	cur, err := s.obligations.Current(ctx, p.ObligationKey)
	if err != nil {
		return PackageView{}, err
	}
	v.ObligationStatus = cur.Status
	if v.Gate, _, err = s.gate(ctx, p); err != nil {
		return PackageView{}, err
	}

	prep := p.Preparation()
	check := func(dim string, ok bool, detail string) {
		v.Readiness = append(v.Readiness, PackageReadiness{Dimension: dim, Ready: ok, Detail: detail})
	}
	// Past submission, the pre-submission dimensions describe what was filed.
	submitted := p.Status.Submitted()
	switch {
	case submitted:
		check("DATA_READY", true, "Filed against the obligation as it stood.")
	case cur.Status == obligation.StatusReady || cur.Status == obligation.StatusRejected:
		check("DATA_READY", true, fmt.Sprintf("The obligation is %s.", cur.Status))
	default:
		check("DATA_READY", false, fmt.Sprintf("The obligation is %s; it is prepared once marked READY.", cur.Status))
	}
	if prep != nil {
		check("WORKPAPER_READY", true, fmt.Sprintf("Version %d prepared.", prep.Version))
	} else {
		check("WORKPAPER_READY", false, "No version has been prepared.")
	}
	switch {
	case prep == nil:
		check("REVIEW_READY", false, "Nothing to review yet.")
	case p.Approval() != nil || submitted:
		check("REVIEW_READY", true, fmt.Sprintf("Version %d reviewed and approved.", prep.Version))
	case p.Status == compliance.StatusInReview:
		check("REVIEW_READY", false, fmt.Sprintf("Version %d awaits an independent review and approval.", prep.Version))
	default:
		check("REVIEW_READY", false, "The version was returned and is prepared again.")
	}
	gateDetail := "The legal gate allows filing now."
	if !v.Gate.Allowed {
		gateDetail = fmt.Sprintf("%s (%s)", v.Gate.Detail, v.Gate.Reason)
	}
	check("REPRESENTATION_READY", v.Gate.Allowed || submitted, gateDetail)

	v.Ready = true
	for _, r := range v.Readiness {
		v.Ready = v.Ready && r.Ready
	}
	return v, nil
}

// sameMoney reports whether a prepared control total is still the figure.
func sameMoney(prepared *fiscal.Money, now fiscal.Money) bool {
	if prepared == nil || prepared.Currency() != now.Currency() {
		return false
	}
	c, err := prepared.Cmp(now)
	return err == nil && c == 0
}

func (s *ComplianceService) inTx(ctx context.Context, fn func(context.Context) error) error {
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
