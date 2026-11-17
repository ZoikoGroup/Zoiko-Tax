package http

import (
	"net/http"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/reconciliation"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// Latest-first listings of decisions, refunds, documents, reconciliation runs
// and batch jobs. Each answers 503 exactly where its single read does.

func toDecisionSummary(l port.DecisionListing) gen.DecisionSummary {
	rec := l.Record
	d := gen.DecisionSummary{
		ID: rec.DecisionID.String(), BusinessKey: rec.BusinessKey,
		Authoritative: rec.Outcome.Filable(),
		Outcome:       gen.Outcome(rec.Outcome),
		ReasonCode:    string(rec.Reason),
		EventTime:     canonical.FormatTime(rec.EventTime),
		RecordedAt:    canonical.FormatTime(rec.RecordedAt),
		Bundle:        gen.BundleRef{BundleID: rec.BundleID, Digest: rec.BundleDigest, IrVersion: saturate32(rec.IRVersion)},
	}
	if rec.Supersedes != nil {
		s := rec.Supersedes.String()
		d.Supersedes = &s
	}
	if l.SupersededBy != nil {
		s := l.SupersededBy.String()
		d.SupersededBy = &s
	}
	return d
}

func (rt *Router) handleListDecisions(w http.ResponseWriter, r *http.Request) {
	if rt.determinationUnavailable(w, r) {
		return
	}
	q := r.URL.Query()
	list, err := rt.Determination.Decisions(r.Context(), port.DecisionFilter{
		BusinessKey: q.Get("businessKey"),
		Outcome:     evidence.Outcome(q.Get("outcome")),
		Limit:       limitOf(r),
	})
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.DecisionSummaryList{Decisions: make([]gen.DecisionSummary, len(list))}
	for i, l := range list {
		out.Decisions[i] = toDecisionSummary(l)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleListRefunds(w http.ResponseWriter, r *http.Request) {
	if rt.refundsUnavailable(w, r) {
		return
	}
	views, err := rt.Refunds.Refunds(r.Context(), limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.RefundList{Refunds: make([]gen.Refund, len(views))}
	for i, v := range views {
		out.Refunds[i] = toRefund(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	if rt.documentsUnavailable(w, r) {
		return
	}
	views, err := rt.Documents.Documents(r.Context(), limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.FiscalDocumentList{Documents: make([]gen.FiscalDocument, len(views))}
	for i, v := range views {
		out.Documents[i] = toDocument(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

// toReconRunSummary counts a run's items rather than listing them. An item
// is an exception unless it matched, exactly or within tolerance.
func toReconRunSummary(v app.RunView) gen.ReconciliationRunSummary {
	full := toReconRun(app.RunView{Run: v.Run})
	exceptions, resolved := 0, 0
	for _, it := range v.Items {
		if it.Status != reconciliation.StatusMatched && it.Status != reconciliation.StatusToleranceMatch {
			exceptions++
		}
		if it.Resolution != nil {
			resolved++
		}
	}
	return gen.ReconciliationRunSummary{
		ID: full.ID, Period: full.Period, LegalEntityID: full.LegalEntityID, RanAt: full.RanAt, RanBy: full.RanBy,
		FirstBreak: full.FirstBreak, Unavailable: full.Unavailable,
		ItemCount: saturate32(len(v.Items)), ExceptionCount: saturate32(exceptions), ResolvedCount: saturate32(resolved),
	}
}

func (rt *Router) handleListReconciliations(w http.ResponseWriter, r *http.Request) {
	if rt.reconUnavailable(w, r) {
		return
	}
	views, err := rt.Reconciliations.Runs(r.Context(), limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.ReconciliationRunList{Runs: make([]gen.ReconciliationRunSummary, len(views))}
	for i, v := range views {
		out.Runs[i] = toReconRunSummary(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

// toJobSummary is a job's progress without its items.
func toJobSummary(v app.JobView) gen.JobSummary {
	j := toJob(v)
	return gen.JobSummary{
		ID: j.ID, Operation: gen.JobSummaryOperation(j.Operation), Status: gen.JobSummaryStatus(j.Status),
		ItemCount: j.ItemCount, Succeeded: j.Succeeded, Failed: j.Failed, Pending: j.Pending,
		RequestedAt: j.RequestedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
	}
}

func (rt *Router) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if rt.batchesUnavailable(w, r) {
		return
	}
	views, err := rt.Batches.Jobs(r.Context(), limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.JobList{Jobs: make([]gen.JobSummary, len(views))}
	for i, v := range views {
		out.Jobs[i] = toJobSummary(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}
