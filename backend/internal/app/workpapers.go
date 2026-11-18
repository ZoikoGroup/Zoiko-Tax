package app

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	pcontrib "github.com/zoikogroup/zoikotax/backend/internal/domain/contribution"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/workpaper"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Regulatory workpapers (OBL-07 Workpaper Assembly & Tax/Regulatory Base
// Ledger; LED-19 tie-out; OPS-15 exception triage). The rules are
// internal/domain/workpaper's; this is where a workpaper meets the people
// acting, the sources a build reads and the store.
//
// A build reads two things that already exist: the provider contribution
// ledger for the filer, duty and period (the schedules and the BSS control
// totals) and the filer's Tax Control Subledger balances for the period (the
// subledger and GL bridge columns). It reads both inside the transaction
// that records the build, so the snapshot a build hashes is the snapshot it
// assembled.
//
// Idempotency is natural, as for contributions. A workpaper is identified by
// its filer, duty and period, so creating it again returns the one already
// made; a build is identified by its inputs hash, so building again on the
// same inputs records nothing. A client-minted Idempotency-Key would be a
// second identity for each.
//
// Roles. OBL-07 names Preparer, Tax Accountant and Reviewer: OPERATOR and
// ANALYST prepare — create, build, document rates, propose adjustments and
// resolutions, freeze, change the basis — and an ANALYST reviews: decides a
// material adjustment or a resolution, and approves a frozen build. Each
// review is also a comparison of people, made by the domain. AUDITOR reads.
// ADMIN reads no fiscal record here, as elsewhere.

// WorkpaperService assembles, reviews and approves regulatory workpapers.
type WorkpaperService struct {
	store         port.WorkpaperRepository
	contributions port.WorkpaperContributionSource
	subledger     port.WorkpaperSubledgerSource
	entities      port.LegalEntityRepository
	tx            port.TxManager
	clock         clock.Clock
	ids           idgen.Generator
}

// NewWorkpaperService wires the service. subledger may be nil, in which case
// builds carry no subledger balances.
func NewWorkpaperService(store port.WorkpaperRepository, contributions port.WorkpaperContributionSource,
	subledger port.WorkpaperSubledgerSource, entities port.LegalEntityRepository, tx port.TxManager, clk clock.Clock,
	ids idgen.Generator) *WorkpaperService {
	return &WorkpaperService{store: store, contributions: contributions, subledger: subledger, entities: entities, tx: tx,
		clock: clk, ids: ids}
}

var (
	workpaperReaders   = []security.Role{security.RoleOperator, security.RoleAnalyst, security.RoleAuditor}
	workpaperPreparers = []security.Role{security.RoleOperator, security.RoleAnalyst}
	workpaperReviewers = []security.Role{security.RoleAnalyst}
)

func (s *WorkpaperService) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }

// WorkpaperView is a workpaper with what its state admits now.
type WorkpaperView struct {
	workpaper.Workpaper
	Exceptions []workpaper.ExceptionState
	Blockers   []workpaper.Blocker
	Actions    []workpaper.Action
}

func viewOf(w workpaper.Workpaper) WorkpaperView {
	return WorkpaperView{Workpaper: w, Exceptions: w.Exceptions(), Blockers: w.Blockers(), Actions: w.AllowedActions()}
}

// WorkpaperInput is a workpaper to open. Amounts are canonical decimal
// strings, parsed here and nowhere else.
type WorkpaperInput struct {
	LegalEntity       id.LegalEntityID // zero for the tenant's default entity
	Duty              pcontrib.Regime
	Period            string
	Authority         string
	ReportingCurrency string
	Materiality       string
	Obligation        id.ObligationID // zero for none
	ContentRelease    string
}

// Create opens a workpaper for a legal filer, duty and period. It is
// idempotent on the three: the same header again returns the workpaper
// already opened, with created false, and a different header is refused
// with WORKPAPER_EXISTS.
func (s *WorkpaperService) Create(ctx context.Context, in WorkpaperInput) (WorkpaperView, bool, error) {
	sc, err := requirePerson(ctx, workpaperPreparers...)
	if err != nil {
		return WorkpaperView{}, false, err
	}
	cur := fiscal.Currency(strings.ToUpper(strings.TrimSpace(in.ReportingCurrency)))
	if !workpaper.ValidCurrency(string(cur)) {
		return WorkpaperView{}, false, errs.Invalid("reportingCurrency", errs.ReasonInvalidValue, "The reporting currency is an ISO 4217 code, such as USD.")
	}
	materiality := strings.TrimSpace(in.Materiality)
	if materiality == "" {
		materiality = "0"
	}
	m, err := workpaper.ParseAmount("materialityThreshold", materiality, cur)
	if err != nil {
		return WorkpaperView{}, false, err
	}
	w := workpaper.Workpaper{
		TenantID: sc.Tenant(), Duty: in.Duty, Period: strings.TrimSpace(in.Period), Authority: strings.TrimSpace(in.Authority),
		ReportingCurrency: cur, Materiality: m, Obligation: in.Obligation, InitialRelease: strings.TrimSpace(in.ContentRelease),
		CreatedBy: sc.Subject(),
	}
	var (
		out     workpaper.Workpaper
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		le, err := s.entity(ctx, in.LegalEntity)
		if err != nil {
			return err
		}
		w.LegalEntity = le
		if err := w.Validate(); err != nil {
			return err
		}
		if err := s.store.Lock(ctx, fmt.Sprintf("key/%s/%s/%s", le, w.Duty, w.Period)); err != nil {
			return err
		}
		prior, found, err := s.store.ByKey(ctx, le, w.Duty, w.Period)
		if err != nil {
			return err
		}
		if found {
			if !prior.SameHeader(w) {
				return errs.New(errs.CategoryConflict, workpaper.ReasonWorkpaperExists,
					fmt.Sprintf("Workpaper %s is already open for this filer, %s and %s, with a different header.", prior.ID, w.Duty, w.Period))
			}
			out, err = s.store.Workpaper(ctx, prior.ID)
			return err
		}
		wid, err := idgen.WorkpaperID(s.ids)
		if err != nil {
			return internal(err, "The workpaper could not be opened.")
		}
		now := s.now()
		w.ID, w.CreatedAt = wid, now
		if err := s.store.Create(ctx, w, workpaper.Created(sc.Subject(), now)); err != nil {
			return err
		}
		created = true
		out, err = s.store.Workpaper(ctx, wid)
		return err
	})
	if err != nil {
		return WorkpaperView{}, false, err
	}
	return viewOf(out), created, nil
}

func (s *WorkpaperService) entity(ctx context.Context, le id.LegalEntityID) (id.LegalEntityID, error) {
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

// Workpapers lists workpapers, newest period first.
func (s *WorkpaperService) Workpapers(ctx context.Context, f workpaper.Filter) ([]WorkpaperView, error) {
	if _, err := requireRoleOrSystem(ctx, workpaperReaders...); err != nil {
		return nil, err
	}
	if f.Period != "" && !workpaper.ValidPeriod(f.Period) {
		return nil, errs.Invalid("period", errs.ReasonInvalidValue, "The period is a month, YYYY-MM.")
	}
	rows, err := s.store.List(ctx, f)
	if err != nil {
		return nil, err
	}
	out := make([]WorkpaperView, len(rows))
	for i, w := range rows {
		out[i] = WorkpaperView{Workpaper: w}
	}
	return out, nil
}

// Workpaper reads one workpaper with its latest build and what it admits.
func (s *WorkpaperService) Workpaper(ctx context.Context, workpaperID id.WorkpaperID) (WorkpaperView, error) {
	if _, err := requireRoleOrSystem(ctx, workpaperReaders...); err != nil {
		return WorkpaperView{}, err
	}
	w, err := s.store.Workpaper(ctx, workpaperID)
	if err != nil {
		return WorkpaperView{}, err
	}
	return viewOf(w), nil
}

func (s *WorkpaperService) sources(ctx context.Context, w workpaper.Workpaper) (workpaper.Sources, error) {
	events, err := s.contributions.InPeriod(ctx, w.Period)
	if err != nil {
		return workpaper.Sources{}, err
	}
	var keys []pcontrib.SourceKey
	for _, c := range events {
		if c.Reverses != nil {
			keys = append(keys, *c.Reverses)
		}
	}
	known, err := s.contributions.KnownSources(ctx, keys)
	if err != nil {
		return workpaper.Sources{}, err
	}
	src := workpaper.Sources{Contributions: events, KnownOriginals: known}
	if s.subledger != nil {
		pop, err := s.subledger.Population(ctx, w.LegalEntity, w.Period)
		if err != nil {
			return workpaper.Sources{}, err
		}
		src.Subledger = pop.Balances
	}
	return src, nil
}

func stale(w workpaper.Workpaper, expectedSeq int) error {
	if expectedSeq > 0 && expectedSeq != len(w.History) {
		return errs.New(errs.CategoryConflict, workpaper.ReasonStale,
			fmt.Sprintf("The workpaper is at event %d, not %d. Reload it and decide again.", len(w.History), expectedSeq))
	}
	return nil
}

// Build assembles the workpaper from its sources now. It is deterministic
// and idempotent: when the inputs hash equals the latest build's, nothing is
// recorded and created is false. Otherwise a new build version is recorded
// with its inputs hash, output and output hash; a rebuild of an approved
// workpaper revokes the approval. A workpaper frozen for review is not
// rebuilt (WORKPAPER_SNAPSHOT_LOCKED).
func (s *WorkpaperService) Build(ctx context.Context, workpaperID id.WorkpaperID, expectedSeq int) (WorkpaperView, bool, error) {
	sc, err := requirePerson(ctx, workpaperPreparers...)
	if err != nil {
		return WorkpaperView{}, false, err
	}
	var (
		out     workpaper.Workpaper
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "id/"+workpaperID.String()); err != nil {
			return err
		}
		w, err := s.store.Workpaper(ctx, workpaperID)
		if err != nil {
			return err
		}
		if err := stale(w, expectedSeq); err != nil {
			return err
		}
		if w.Status == workpaper.StatusReadyForReview {
			return errs.New(errs.CategoryConflict, workpaper.ReasonSnapshotLocked,
				"The workpaper is frozen for review: its source snapshot is locked. Return it to draft to rebuild.")
		}
		src, err := s.sources(ctx, w)
		if err != nil {
			return err
		}
		a, err := workpaper.Assemble(w, src)
		if err != nil {
			return internal(err, "The workpaper could not be assembled.")
		}
		if latest := w.Latest(); latest != nil && latest.InputsHash.Equal(a.InputsHash) {
			out = w
			return nil
		}
		now := s.now()
		b := workpaper.Build{Version: len(w.Builds) + 1, InputsHash: a.InputsHash, OutputHash: a.OutputHash,
			BasisDigest: a.BasisDigest, FormulaVersion: workpaper.FormulaVersion, ContentRelease: w.ContentRelease,
			BuiltAt: now, BuiltBy: sc.Subject(), Output: &a.Output}
		events, err := w.RecordBuild(b, sc.Subject(), now)
		if err != nil {
			return err
		}
		if err := s.store.AppendBuild(ctx, w.ID, b); err != nil {
			return err
		}
		if err := s.store.AppendEvents(ctx, w.ID, events); err != nil {
			return err
		}
		created = true
		out, err = s.store.Workpaper(ctx, w.ID)
		return err
	})
	if err != nil {
		return WorkpaperView{}, false, err
	}
	return viewOf(out), created, nil
}

// SourceCheck compares the latest build with what the sources would build
// now: whether the frozen or latest snapshot still matches its sources.
type SourceCheck struct {
	LatestVersion     int
	LatestInputsHash  canonical.Digest
	LatestOutputHash  canonical.Digest
	CurrentInputsHash canonical.Digest
	CurrentOutput     canonical.Digest
	FrozenVersion     int
	Matches           bool
	CheckedAt         time.Time
}

// SourceCheck assembles the workpaper from its sources now without
// recording anything, and compares the result with the latest build.
func (s *WorkpaperService) SourceCheck(ctx context.Context, workpaperID id.WorkpaperID) (SourceCheck, error) {
	if _, err := requireRoleOrSystem(ctx, workpaperReaders...); err != nil {
		return SourceCheck{}, err
	}
	var out SourceCheck
	err := s.inTx(ctx, func(ctx context.Context) error {
		w, err := s.store.Workpaper(ctx, workpaperID)
		if err != nil {
			return err
		}
		src, err := s.sources(ctx, w)
		if err != nil {
			return err
		}
		a, err := workpaper.Assemble(w, src)
		if err != nil {
			return internal(err, "The workpaper could not be assembled.")
		}
		out = SourceCheck{CurrentInputsHash: a.InputsHash, CurrentOutput: a.OutputHash, FrozenVersion: w.FrozenBuild, CheckedAt: s.now()}
		if b := w.Latest(); b != nil {
			out.LatestVersion, out.LatestInputsHash, out.LatestOutputHash = b.Version, b.InputsHash, b.OutputHash
			out.Matches = b.InputsHash.Equal(a.InputsHash)
		}
		return nil
	})
	return out, err
}

// FXInput is a rate to document, as sent.
type FXInput struct {
	Currency        string
	Rate            string
	RateSource      string
	RateDate        string
	RoundingMode    string
	Scale           int
	ConversionBasis string
}

// AdjustmentInput is an adjustment to propose, as sent.
type AdjustmentInput struct {
	Kind     workpaper.AdjustmentKind
	Category string
	Amount   string
	Reason   string
}

// WorkpaperAct is one act on a workpaper. The fields an action does not use
// are ignored.
type WorkpaperAct struct {
	Action workpaper.Action
	// ExpectedSeq is the number of events the caller read; zero skips the
	// check.
	ExpectedSeq      int
	Note             string
	BuildVersion     int
	ContentRelease   string
	FX               *FXInput
	Adjustment       *AdjustmentInput
	AdjustmentNumber int
	Decision         workpaper.Decision
	ExceptionKey     string
	Fingerprint      string
	Evidence         *workpaper.Evidence
}

func parseFX(in *FXInput) (workpaper.FXRate, error) {
	if in == nil {
		return workpaper.FXRate{}, errs.Invalid("fx", errs.ReasonMissingField, "Documenting a rate needs the rate.")
	}
	rateText := strings.TrimSpace(in.Rate)
	if !decimalText(rateText) {
		return workpaper.FXRate{}, errs.Invalid("fx.rate", errs.ReasonInvalidValue, "A rate is a canonical decimal string, such as \"1.0842\".")
	}
	rate, err := fiscal.ParseRate(rateText, fiscal.RateBasisNet)
	if err != nil {
		return workpaper.FXRate{}, errs.Invalid("fx.rate", errs.ReasonInvalidValue, "The rate is not representable exactly.")
	}
	day, err := time.Parse(time.DateOnly, strings.TrimSpace(in.RateDate))
	if err != nil {
		return workpaper.FXRate{}, errs.Invalid("fx.rateDate", errs.ReasonInvalidValue, "A rate date is YYYY-MM-DD.")
	}
	if in.Scale < 0 || in.Scale > fiscal.MaxScale {
		return workpaper.FXRate{}, errs.Invalid("fx.scale", errs.ReasonInvalidValue, fmt.Sprintf("The rounding scale is 0 to %d.", fiscal.MaxScale))
	}
	return workpaper.FXRate{
		Currency: fiscal.Currency(strings.ToUpper(strings.TrimSpace(in.Currency))), Rate: rate, Source: in.RateSource,
		RateDate: day, RoundingMode: fiscal.RoundingMode(strings.TrimSpace(in.RoundingMode)), Scale: int32(in.Scale), //nolint:gosec // bounded above
		Basis: in.ConversionBasis,
	}, nil
}

var rateShape = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func decimalText(s string) bool { return rateShape.MatchString(s) }

// Act applies one act to a workpaper under its lock, as a person.
func (s *WorkpaperService) Act(ctx context.Context, workpaperID id.WorkpaperID, a WorkpaperAct) (WorkpaperView, error) {
	roles := workpaperPreparers
	switch a.Action {
	case workpaper.ActionApprove, workpaper.ActionDecideAdjustment, workpaper.ActionDecideResolution:
		roles = workpaperReviewers
	case workpaper.ActionBuild:
		return WorkpaperView{}, errs.Invalid("action", errs.ReasonInvalidValue, "A build is its own request: POST .../build.")
	case workpaper.ActionFreeze, workpaper.ActionReturnToDraft, workpaper.ActionChangeBasis, workpaper.ActionDocumentFX,
		workpaper.ActionProposeAdjustment, workpaper.ActionProposeResolution:
	default:
		return WorkpaperView{}, errs.Invalid("action", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a workpaper action.", a.Action))
	}
	sc, err := requirePerson(ctx, roles...)
	if err != nil {
		return WorkpaperView{}, err
	}
	var out workpaper.Workpaper
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "id/"+workpaperID.String()); err != nil {
			return err
		}
		// Read under the lock: an act decided against a history somebody has
		// since extended is decided against the wrong one.
		w, err := s.store.Workpaper(ctx, workpaperID)
		if err != nil {
			return err
		}
		if err := stale(w, a.ExpectedSeq); err != nil {
			return err
		}
		events, err := s.decide(w, a, sc.Subject(), s.now())
		if err != nil {
			return err
		}
		if err := s.store.AppendEvents(ctx, w.ID, events); err != nil {
			return err
		}
		out, err = s.store.Workpaper(ctx, w.ID)
		return err
	})
	if err != nil {
		return WorkpaperView{}, err
	}
	return viewOf(out), nil
}

func (s *WorkpaperService) decide(w workpaper.Workpaper, a WorkpaperAct, by id.UserID, at time.Time) ([]workpaper.Event, error) {
	one := func(e workpaper.Event, err error) ([]workpaper.Event, error) {
		if err != nil {
			return nil, err
		}
		return []workpaper.Event{e}, nil
	}
	switch a.Action {
	case workpaper.ActionFreeze:
		return one(w.Freeze(a.Note, by, at))
	case workpaper.ActionReturnToDraft:
		return one(w.ReturnToDraft(a.Note, by, at))
	case workpaper.ActionApprove:
		return one(w.Approve(a.BuildVersion, a.Note, by, at))
	case workpaper.ActionChangeBasis:
		return one(w.ChangeBasis(a.ContentRelease, a.Note, by, at))
	case workpaper.ActionDocumentFX:
		fx, err := parseFX(a.FX)
		if err != nil {
			return nil, err
		}
		return one(w.DocumentFX(fx, by, at))
	case workpaper.ActionProposeAdjustment:
		if a.Adjustment == nil {
			return nil, errs.Invalid("adjustment", errs.ReasonMissingField, "Proposing an adjustment needs the adjustment.")
		}
		amount, err := workpaper.ParseAmount("adjustment.amount", strings.TrimSpace(a.Adjustment.Amount), w.ReportingCurrency)
		if err != nil {
			return nil, err
		}
		return one(w.ProposeAdjustment(workpaper.AdjustmentProposal{Kind: a.Adjustment.Kind, Category: a.Adjustment.Category,
			Amount: amount, Reason: a.Adjustment.Reason, Evidence: a.Evidence}, by, at))
	case workpaper.ActionDecideAdjustment:
		return one(w.DecideAdjustment(a.AdjustmentNumber, a.Decision, a.Note, by, at))
	case workpaper.ActionProposeResolution:
		return one(w.ProposeResolution(strings.TrimSpace(a.ExceptionKey), strings.TrimSpace(a.Fingerprint), a.Note, a.Evidence, by, at))
	case workpaper.ActionDecideResolution:
		return one(w.DecideResolution(strings.TrimSpace(a.ExceptionKey), a.Decision, a.Note, by, at))
	}
	return nil, errs.Invalid("action", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a workpaper action.", a.Action))
}

func (s *WorkpaperService) inTx(ctx context.Context, fn func(context.Context) error) error {
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
