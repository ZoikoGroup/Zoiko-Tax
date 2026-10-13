package app

import (
	"cmp"
	"context"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/legal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// The Tax Command Center. It reads what the other services wrote and writes
// nothing: every figure is a sum of rows another endpoint returns one by
// one, so the dashboard cannot disagree with the record it summarizes.
//
// It needs no content bundle. A cell that has recorded nothing reports
// zeros, which is the truth about it, rather than refusing.

// Dashboard bounds.
const (
	// DashboardWindowDays is how far ahead the obligation window looks.
	DashboardWindowDays = 30
	// DashboardTrendPeriods is how many periods the trend covers.
	DashboardTrendPeriods = 8
	// dashboardActionLimit caps the action list.
	dashboardActionLimit = 25
	// dashboardLookback is how long a final failure stays an action.
	dashboardLookback = 30 * 24 * time.Hour
	// dashboardObligationCap is the most obligations the window reads.
	dashboardObligationCap = 500
)

var legalPeriodShape = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

// DashboardService assembles the Tax Command Center.
type DashboardService struct {
	repo        port.DashboardRepository
	entities    port.LegalEntityRepository
	obligations port.ObligationRepository
	auths       port.AuthorizationRepository
	matrix      legal.Matrix
	clock       clock.Clock
}

// NewDashboardService wires the dashboard over the stores it reads and the
// LegalAuthorization matrix the cell loaded.
func NewDashboardService(repo port.DashboardRepository, entities port.LegalEntityRepository,
	obligations port.ObligationRepository, auths port.AuthorizationRepository, matrix legal.Matrix,
	clk clock.Clock) *DashboardService {
	return &DashboardService{repo: repo, entities: entities, obligations: obligations, auths: auths, matrix: matrix, clock: clk}
}

// Dashboard is the Tax Command Center for one legal period.
type Dashboard struct {
	AsOf           time.Time
	Period         string
	LegalEntity    id.LegalEntityID
	Determinations DashboardDeterminations
	// Liability is TAX_COLLECTED_LIABILITY for the period, by currency.
	Liability      []DashboardBalance
	Obligations    DashboardObligations
	Reconciliation port.ReconciliationSummary
	Evidence       port.SealCoverage
	Jurisdictions  DashboardJurisdictions
	Actions        []DashboardAction
	Trend          DashboardTrend
}

// DashboardDeterminations are the period's decisions and the one before.
type DashboardDeterminations struct {
	Decisions         int
	Tax               []fiscal.Money
	PreviousPeriod    string
	PreviousDecisions int
	PreviousTax       []fiscal.Money
	ReviewRequired    int
}

// DashboardBalance is one currency of one account, debits and credits apart.
type DashboardBalance struct {
	Account subledger.Account
	Debits  fiscal.Money
	Credits fiscal.Money
}

// DashboardObligations is the obligation window.
type DashboardObligations struct {
	WindowDays int
	Due        int
	Overdue    int
	Amounts    []fiscal.Money
}

// Readiness is whether filing with an authority could proceed now.
type Readiness string

// The three readinesses.
const (
	ReadinessReady   Readiness = "READY"
	ReadinessAtRisk  Readiness = "AT_RISK"
	ReadinessBlocked Readiness = "BLOCKED"
)

// DashboardAuthority is one authority the matrix names.
type DashboardAuthority struct {
	Country   string
	Authority string
	Readiness Readiness
	Reason    legal.Reason
}

// DashboardJurisdictions counts authorities by readiness.
type DashboardJurisdictions struct {
	Ready, AtRisk, Blocked int
	Authorities            []DashboardAuthority
}

// ActionKind is what an action concerns.
type ActionKind string

// The action kinds.
const (
	ActionObligationOverdue   ActionKind = "OBLIGATION_OVERDUE"
	ActionObligationAttention ActionKind = "OBLIGATION_ATTENTION"
	ActionObligationDue       ActionKind = "OBLIGATION_DUE"
	ActionReconciliationBreak ActionKind = "RECONCILIATION_BREAK"
	ActionRefundUncertain     ActionKind = "REFUND_UNCERTAIN"
	ActionRefundFailed        ActionKind = "REFUND_FAILED"
	ActionDecisionReview      ActionKind = "DECISION_REVIEW"
	ActionWebhookDead         ActionKind = "WEBHOOK_DEAD"
)

// Priority orders actions: P0 now, P1 this week, P2 this month.
type Priority string

// The priorities.
const (
	P0 Priority = "P0"
	P1 Priority = "P1"
	P2 Priority = "P2"
)

// DashboardAction is one thing that needs a person.
type DashboardAction struct {
	Kind     ActionKind
	Priority Priority
	Title    string
	Context  string
	Exposure *fiscal.Money
	// Due is a civil date, YYYY-MM-DD, in the subject's legal calendar.
	Due       string
	SubjectID string
}

// DashboardTrend is the periods ending with the reported one, oldest first.
type DashboardTrend struct {
	// Currency is the currency the liability series is in; empty when
	// nothing posted.
	Currency fiscal.Currency
	Points   []DashboardTrendPoint
}

// DashboardTrendPoint is one period of the trend.
type DashboardTrendPoint struct {
	Period    string
	Decisions int
	// Liability is nil when nothing was credited in the trend currency.
	Liability *fiscal.Money
	// Index is Liability as a share of the largest point, 0–100.
	Index int
}

// Dashboard assembles the Tax Command Center for period, YYYY-MM; empty is
// the current month in UTC.
func (s *DashboardService) Dashboard(ctx context.Context, period string) (Dashboard, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return Dashboard{}, err
	}
	now := s.clock.Now().UTC().Truncate(time.Microsecond)
	start, err := periodStart(period, now)
	if err != nil {
		return Dashboard{}, err
	}
	period = start.Format("2006-01")
	end := start.AddDate(0, 1, 0)
	prev := start.AddDate(0, -1, 0).Format("2006-01")
	first := start.AddDate(0, 1-DashboardTrendPeriods, 0)

	le, err := s.entities.Default(ctx)
	if err != nil {
		return Dashboard{}, err
	}
	decisions, err := s.repo.Decisions(ctx, first, end)
	if err != nil {
		return Dashboard{}, err
	}
	postings, err := s.repo.Postings(ctx, le.ID, subledger.AccountTaxCollectedLiability, first.Format("2006-01"), period)
	if err != nil {
		return Dashboard{}, err
	}
	ledger, err := foldPostings(postings)
	if err != nil {
		return Dashboard{}, err
	}

	d := Dashboard{
		AsOf:        now,
		Period:      period,
		LegalEntity: le.ID,
		Determinations: DashboardDeterminations{
			Decisions:         decisions[period].Recorded,
			Tax:               ledger.creditsIn(period),
			PreviousPeriod:    prev,
			PreviousDecisions: decisions[prev].Recorded,
			PreviousTax:       ledger.creditsIn(prev),
			ReviewRequired:    decisions[period].ReviewRequired,
		},
		Liability: ledger.balances(period),
	}
	if d.Trend, err = ledger.trend(first, DashboardTrendPeriods, decisions); err != nil {
		return Dashboard{}, err
	}

	var actions []DashboardAction
	if d.Obligations, actions, err = s.obligationWindow(ctx, le.ID, now); err != nil {
		return Dashboard{}, err
	}
	summary, breaks, err := s.repo.Reconciliation(ctx, le.ID, dashboardActionLimit)
	if err != nil {
		return Dashboard{}, err
	}
	d.Reconciliation = summary
	for _, b := range breaks {
		actions = append(actions, DashboardAction{
			Kind: ActionReconciliationBreak, Priority: P1,
			Title:    b.Stage + " " + b.Status,
			Context:  b.Period + " | " + b.MatchKey,
			Exposure: b.Variance, SubjectID: b.ItemID,
		})
	}
	if d.Evidence, err = s.repo.SealCoverage(ctx); err != nil {
		return Dashboard{}, err
	}
	refunds, err := s.repo.RefundsAwaiting(ctx, now.Add(-dashboardLookback), dashboardActionLimit)
	if err != nil {
		return Dashboard{}, err
	}
	for _, r := range refunds {
		a := DashboardAction{
			Kind: ActionRefundFailed, Priority: P1, Title: "Refund failed; its tax may be refunded again",
			Context:  r.PaymentReference + " | requested " + r.RequestedAt.Format(time.DateOnly),
			Exposure: &r.Amount, SubjectID: r.RefundID,
		}
		if r.Status == "UNCERTAIN" {
			a.Kind, a.Priority, a.Title = ActionRefundUncertain, P0, "Refund outcome unknown; the money may have moved"
		}
		actions = append(actions, a)
	}
	if n := decisions[period].ReviewRequired; n > 0 {
		actions = append(actions, DashboardAction{
			Kind: ActionDecisionReview, Priority: P1,
			Title: plural(n, "decision needs", "decisions need") + " review", Context: period,
		})
	}
	dead, err := s.repo.DeadDeliveries(ctx, now.Add(-dashboardLookback))
	if err != nil {
		return Dashboard{}, err
	}
	if dead > 0 {
		actions = append(actions, DashboardAction{
			Kind: ActionWebhookDead, Priority: P2,
			Title: plural(dead, "webhook delivery", "webhook deliveries") + " dead-lettered", Context: "last 30 days",
		})
	}
	d.Actions = rank(actions)

	if d.Jurisdictions, err = s.jurisdictions(ctx, le.ID, period, now); err != nil {
		return Dashboard{}, err
	}
	return d, nil
}

// periodStart parses a YYYY-MM period, or takes now's month for "".
func periodStart(period string, now time.Time) (time.Time, error) {
	if period == "" {
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), nil
	}
	if !legalPeriodShape.MatchString(period) {
		return time.Time{}, errs.Invalid("period", errs.ReasonInvalidValue, "A period is a month, YYYY-MM.")
	}
	t, err := time.Parse("2006-01", period)
	if err != nil {
		return time.Time{}, errs.Invalid("period", errs.ReasonInvalidValue, "A period is a month, YYYY-MM.")
	}
	return t, nil
}

// obligationWindow counts the default entity's unfiled obligations due
// within the window, overdue included, and the actions they need.
func (s *DashboardService) obligationWindow(ctx context.Context, le id.LegalEntityID, now time.Time) (DashboardObligations, []DashboardAction, error) {
	out := DashboardObligations{WindowDays: DashboardWindowDays}
	rows, err := s.obligations.List(ctx, port.ObligationFilter{Limit: dashboardObligationCap})
	if err != nil {
		return out, nil, err
	}
	rows = slices.DeleteFunc(rows, func(o obligation.Obligation) bool { return o.LegalEntity != le })
	keys := make([]string, len(rows))
	for i, o := range rows {
		keys[i] = o.BusinessKey
	}
	sums, err := s.obligations.Assessed(ctx, keys)
	if err != nil {
		return out, nil, err
	}
	amounts := moneyByCurrency{}
	var actions []DashboardAction
	for _, o := range rows {
		due := o.Period.Due
		if !o.Due.Legal.IsZero() {
			due = o.Due.Legal
		}
		// Today in the obligation's own legal calendar: a return due on the
		// 20th is not overdue on the 20th.
		local := now.In(due.Location())
		today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
		days := int(due.Sub(today).Hours() / 24)
		status := o.EffectiveStatus(today)

		a := DashboardAction{
			Title:     o.Type + " " + humanStatus(status),
			Context:   o.JurisdictionID + " | " + o.Period.Start.Format(time.DateOnly) + " to " + o.Period.End.Format(time.DateOnly),
			Due:       due.Format(time.DateOnly),
			SubjectID: o.ID.String(),
		}
		if m, ok := sums[o.BusinessKey]; ok {
			a.Exposure = &m
		}
		switch status {
		case obligation.StatusOverdue:
			a.Kind, a.Priority = ActionObligationOverdue, P0
		case obligation.StatusRejected, obligation.StatusUncertain:
			a.Kind, a.Priority = ActionObligationAttention, P0
		case obligation.StatusAmendmentRequired, obligation.StatusDataRequired:
			a.Kind, a.Priority = ActionObligationAttention, P1
		}
		if !filed(status) && days <= DashboardWindowDays {
			out.Due++
			if status == obligation.StatusOverdue {
				out.Overdue++
			}
			if a.Exposure != nil {
				if err := amounts.add(*a.Exposure); err != nil {
					return out, nil, err
				}
			}
			if a.Kind == "" {
				a.Kind, a.Priority = ActionObligationDue, P2
				if days <= 7 {
					a.Priority = P1
				}
			}
		}
		if a.Kind != "" {
			actions = append(actions, a)
		}
	}
	out.Amounts = amounts.sorted()
	return out, actions, nil
}

// filed reports whether an obligation is past the point a window counts it.
func filed(s obligation.Status) bool {
	switch s {
	case obligation.StatusFiled, obligation.StatusAccepted, obligation.StatusClosed, obligation.StatusPaid,
		obligation.StatusPaymentDue, obligation.StatusUncertain:
		return true
	}
	return false
}

func humanStatus(s obligation.Status) string {
	switch s {
	case obligation.StatusOverdue:
		return "overdue"
	case obligation.StatusRejected:
		return "rejected by the authority"
	case obligation.StatusUncertain:
		return "submission outcome unknown"
	case obligation.StatusAmendmentRequired:
		return "needs an amendment"
	case obligation.StatusDataRequired:
		return "needs data"
	}
	return "due"
}

// jurisdictions resolves FILE for every authority the matrix names, through
// the same gate an action passes (ZTAX-LEG-REQ-0001).
func (s *DashboardService) jurisdictions(ctx context.Context, le id.LegalEntityID, period string, now time.Time) (DashboardJurisdictions, error) {
	out := DashboardJurisdictions{Authorities: []DashboardAuthority{}}
	type key struct{ country, authority string }
	seen := map[key]bool{}
	var names []key
	for _, r := range s.matrix.Rules {
		k := key{r.Country, r.Authority}
		if !seen[k] {
			seen[k] = true
			names = append(names, k)
		}
	}
	if len(names) == 0 {
		return out, nil
	}
	slices.SortFunc(names, func(a, b key) int {
		return cmp.Or(cmp.Compare(a.country, b.country), cmp.Compare(a.authority, b.authority))
	})
	auths, err := s.auths.List(ctx)
	if err != nil {
		return out, err
	}
	for _, k := range names {
		res := legal.Resolve(s.matrix, auths, legal.Action{
			Country: k.country, Authority: k.authority, Service: legal.ServiceFile, LegalEntity: le, Period: period,
		}, now)
		a := DashboardAuthority{Country: k.country, Authority: k.authority, Reason: res.Reason}
		switch {
		case res.Allowed:
			a.Readiness, a.Reason = ReadinessReady, ""
			out.Ready++
		case awaitsCustomer(res.Reason):
			a.Readiness = ReadinessAtRisk
			out.AtRisk++
		default:
			a.Readiness = ReadinessBlocked
			out.Blocked++
		}
		out.Authorities = append(out.Authorities, a)
	}
	return out, nil
}

// awaitsCustomer reports whether only the customer's grant stands between
// the gate and filing.
func awaitsCustomer(r legal.Reason) bool {
	switch r {
	case legal.ReasonAuthMissing, legal.ReasonAuthExpired, legal.ReasonAuthRevoked, legal.ReasonAuthOutOfScope,
		legal.ReasonCredentialMissing:
		return true
	}
	return false
}

var priorityRank = map[Priority]int{P0: 0, P1: 1, P2: 2}

// rank orders actions most urgent first — priority, then due date, earliest
// first and undated last — and keeps the first dashboardActionLimit.
func rank(actions []DashboardAction) []DashboardAction {
	slices.SortStableFunc(actions, func(a, b DashboardAction) int {
		if c := cmp.Compare(priorityRank[a.Priority], priorityRank[b.Priority]); c != 0 {
			return c
		}
		switch {
		case a.Due == b.Due:
			return 0
		case a.Due == "":
			return 1
		case b.Due == "":
			return -1
		}
		return cmp.Compare(a.Due, b.Due)
	})
	if len(actions) > dashboardActionLimit {
		actions = actions[:dashboardActionLimit]
	}
	if actions == nil {
		return []DashboardAction{}
	}
	return actions
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ---------------------------------------------------------------------------
// sums per currency
// ---------------------------------------------------------------------------

// moneyByCurrency sums amounts per currency, and never across currencies.
type moneyByCurrency map[fiscal.Currency]fiscal.Money

func (m moneyByCurrency) add(v fiscal.Money) error {
	cur, ok := m[v.Currency()]
	if !ok {
		m[v.Currency()] = v
		return nil
	}
	sum, err := cur.Add(v)
	if err != nil {
		return internal(err, "The dashboard could not sum an amount.")
	}
	m[v.Currency()] = sum
	return nil
}

func (m moneyByCurrency) sorted() []fiscal.Money {
	out := make([]fiscal.Money, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b fiscal.Money) int { return cmp.Compare(a.Currency(), b.Currency()) })
	return out
}

// ledgerSums is one account's postings, folded by period and currency.
type ledgerSums struct {
	credits, debits map[string]moneyByCurrency
	// lines counts journal lines per currency, to pick the trend currency.
	lines map[fiscal.Currency]int
}

func foldPostings(ps []port.Posting) (ledgerSums, error) {
	l := ledgerSums{credits: map[string]moneyByCurrency{}, debits: map[string]moneyByCurrency{}, lines: map[fiscal.Currency]int{}}
	for _, p := range ps {
		side := l.credits
		if p.Side == subledger.Debit {
			side = l.debits
		}
		if side[p.Period] == nil {
			side[p.Period] = moneyByCurrency{}
		}
		if err := side[p.Period].add(p.Amount); err != nil {
			return ledgerSums{}, err
		}
		l.lines[p.Amount.Currency()] += p.Lines
	}
	return l, nil
}

func (l ledgerSums) creditsIn(period string) []fiscal.Money { return l.credits[period].sorted() }

// balances reports the period per currency, debits and credits apart, a
// missing side as a zero in that currency.
func (l ledgerSums) balances(period string) []DashboardBalance {
	byCur := map[fiscal.Currency]*DashboardBalance{}
	for _, side := range []struct {
		sums   moneyByCurrency
		credit bool
	}{{l.credits[period], true}, {l.debits[period], false}} {
		for c, m := range side.sums {
			b, ok := byCur[c]
			if !ok {
				b = &DashboardBalance{Account: subledger.AccountTaxCollectedLiability, Debits: m.Zero(), Credits: m.Zero()}
				byCur[c] = b
			}
			if side.credit {
				b.Credits = m
			} else {
				b.Debits = m
			}
		}
	}
	out := make([]DashboardBalance, 0, len(byCur))
	for _, b := range byCur {
		out = append(out, *b)
	}
	slices.SortFunc(out, func(a, b DashboardBalance) int { return cmp.Compare(a.Credits.Currency(), b.Credits.Currency()) })
	return out
}

// trend builds n periods from first, the liability series in the currency
// most lines posted in, ties to the alphabetically first.
func (l ledgerSums) trend(first time.Time, n int, decisions map[string]port.DecisionCount) (DashboardTrend, error) {
	t := DashboardTrend{Points: make([]DashboardTrendPoint, n)}
	best := 0
	for c, lines := range l.lines {
		if lines > best || (lines == best && c < t.Currency) {
			t.Currency, best = c, lines
		}
	}
	var largest *big.Rat
	values := make([]*big.Rat, n)
	for i := range n {
		p := first.AddDate(0, i, 0).Format("2006-01")
		t.Points[i] = DashboardTrendPoint{Period: p, Decisions: decisions[p].Recorded}
		if t.Currency == "" {
			continue
		}
		m, ok := l.credits[p][t.Currency]
		if !ok {
			continue
		}
		t.Points[i].Liability = &m
		v, ok := new(big.Rat).SetString(m.CanonicalString())
		if !ok {
			return DashboardTrend{}, internal(fmt.Errorf("unparseable amount %q", m.CanonicalString()), "The dashboard could not draw the trend.")
		}
		values[i] = v
		if largest == nil || v.Cmp(largest) > 0 {
			largest = v
		}
	}
	if largest == nil || largest.Sign() <= 0 {
		return t, nil
	}
	// Exact rational arithmetic, rounded half up: the index is for drawing,
	// and still never passes through a float.
	hundred, half := big.NewRat(100, 1), big.NewRat(1, 2)
	for i, v := range values {
		if v == nil || v.Sign() <= 0 {
			continue
		}
		q := new(big.Rat).Mul(v, hundred)
		q.Quo(q, largest)
		q.Add(q, half)
		idx := new(big.Int).Quo(q.Num(), q.Denom())
		t.Points[i].Index = int(idx.Int64())
	}
	return t, nil
}
