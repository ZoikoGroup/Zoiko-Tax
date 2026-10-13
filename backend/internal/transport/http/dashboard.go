package http

import (
	"net/http"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The Tax Command Center: read-only aggregates over what the other surfaces
// wrote.

func (rt *Router) dashboardUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Dashboard != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"The dashboard is not wired in this cell."))
	return true
}

func (rt *Router) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if rt.dashboardUnavailable(w, r) {
		return
	}
	d, err := rt.Dashboard.Dashboard(r.Context(), r.URL.Query().Get("period"))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toDashboard(d))
}

func toMoneyValue(m fiscal.Money) gen.MoneyValue {
	return gen.MoneyValue{Amount: m.CanonicalString(), Currency: string(m.Currency())}
}

func toMoneyValues(ms []fiscal.Money) []gen.MoneyValue {
	out := make([]gen.MoneyValue, len(ms))
	for i, m := range ms {
		out[i] = toMoneyValue(m)
	}
	return out
}

func optMoneyValue(m *fiscal.Money) *gen.MoneyValue {
	if m == nil {
		return nil
	}
	v := toMoneyValue(*m)
	return &v
}

func toDashboard(d app.Dashboard) gen.Dashboard {
	out := gen.Dashboard{
		AsOf:          canonical.FormatTime(d.AsOf),
		Period:        d.Period,
		LegalEntityID: d.LegalEntity.String(),
		Determinations: gen.DashboardDeterminations{
			Decisions:         int32(d.Determinations.Decisions), //nolint:gosec // G115: a count of rows, far below 2^31
			Tax:               toMoneyValues(d.Determinations.Tax),
			PreviousPeriod:    d.Determinations.PreviousPeriod,
			PreviousDecisions: int32(d.Determinations.PreviousDecisions), //nolint:gosec // G115: as above
			PreviousTax:       toMoneyValues(d.Determinations.PreviousTax),
			ReviewRequired:    int32(d.Determinations.ReviewRequired), //nolint:gosec // G115: as above
		},
		Liability: make([]gen.ControlBalance, len(d.Liability)),
		Obligations: gen.DashboardObligations{
			WindowDays: int32(d.Obligations.WindowDays), //nolint:gosec // G115: a constant
			Due:        int32(d.Obligations.Due),        //nolint:gosec // G115: at most the listing cap
			Overdue:    int32(d.Obligations.Overdue),    //nolint:gosec // G115: as above
			Amounts:    toMoneyValues(d.Obligations.Amounts),
		},
		Reconciliation: gen.DashboardReconciliation{
			Runs:    int32(d.Reconciliation.Runs),    //nolint:gosec // G115: a count of rows
			Matched: int32(d.Reconciliation.Matched), //nolint:gosec // G115: as above
			Breaks:  int32(d.Reconciliation.Breaks),  //nolint:gosec // G115: as above
			Open:    int32(d.Reconciliation.Open),    //nolint:gosec // G115: as above
		},
		Evidence: gen.DashboardEvidence{
			Decisions: int32(d.Evidence.Decisions), //nolint:gosec // G115: a count of rows
			Sealed:    int32(d.Evidence.Sealed),    //nolint:gosec // G115: as above
			Seals:     int32(d.Evidence.Seals),     //nolint:gosec // G115: as above
		},
		Jurisdictions: gen.DashboardJurisdictions{
			Ready:       int32(d.Jurisdictions.Ready),   //nolint:gosec // G115: authorities in a matrix
			AtRisk:      int32(d.Jurisdictions.AtRisk),  //nolint:gosec // G115: as above
			Blocked:     int32(d.Jurisdictions.Blocked), //nolint:gosec // G115: as above
			Authorities: make([]gen.DashboardAuthority, len(d.Jurisdictions.Authorities)),
		},
		Actions: make([]gen.DashboardAction, len(d.Actions)),
		Trend:   gen.DashboardTrend{Points: make([]gen.DashboardTrendPoint, len(d.Trend.Points))},
	}
	for i, b := range d.Liability {
		out.Liability[i] = gen.ControlBalance{
			Account: gen.ControlAccount(b.Account), Currency: string(b.Credits.Currency()),
			Debits: b.Debits.CanonicalString(), Credits: b.Credits.CanonicalString(),
		}
	}
	for i, a := range d.Jurisdictions.Authorities {
		out.Jurisdictions.Authorities[i] = gen.DashboardAuthority{
			Country: a.Country, Authority: a.Authority, Readiness: gen.DashboardReadiness(a.Readiness),
			Reason: optional(string(a.Reason)),
		}
	}
	for i, a := range d.Actions {
		out.Actions[i] = gen.DashboardAction{
			Kind: gen.DashboardActionKind(a.Kind), Priority: gen.DashboardPriority(a.Priority),
			Title: a.Title, Context: a.Context, Exposure: optMoneyValue(a.Exposure),
			Due: optional(a.Due), SubjectID: optional(a.SubjectID),
		}
	}
	if d.Trend.Currency != "" {
		c := string(d.Trend.Currency)
		out.Trend.Currency = &c
	}
	for i, p := range d.Trend.Points {
		out.Trend.Points[i] = gen.DashboardTrendPoint{
			Period: p.Period, Decisions: int32(p.Decisions), //nolint:gosec // G115: a count of rows
			Liability: optMoneyValue(p.Liability), LiabilityIndex: int32(p.Index), //nolint:gosec // G115: 0–100
		}
	}
	return out
}
