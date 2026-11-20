package http

import (
	"net/http"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/erp"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The ERP journal bridge and GL reconciliation surface (LED-23, LED-18; R6).
// Amounts cross it as the canonical decimal strings the service computed;
// nothing here does arithmetic on one.

func (rt *Router) erpUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.ERP != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"The ERP journal bridge is not wired in this cell. The request was not applied."))
	return true
}

func toERPBridge(b erp.Bridge) gen.ERPBridge {
	out := gen.ERPBridge{
		ID: b.ID.String(), LegalEntityID: b.LegalEntity.String(), Name: b.Name, Vendor: gen.ERPVendor(b.Vendor),
		CompanyCode: b.CompanyCode, CreatedAt: canonical.FormatTime(b.CreatedAt), CreatedBy: b.CreatedBy.String(),
		Mappings: make([]gen.ERPMapping, len(b.Mappings)),
	}
	for i, m := range b.Mappings {
		gm := gen.ERPMapping{Version: saturate32(m.Version), ChartVersion: m.ChartVersion, Digest: m.Digest.String(),
			RecordedAt: canonical.FormatTime(m.RecordedAt), RecordedBy: m.RecordedBy.String(),
			Accounts: make([]gen.ERPAccountMap, len(m.Accounts))}
		for j, am := range m.Accounts {
			gm.Accounts[j] = gen.ERPAccountMap{Account: gen.ControlAccount(am.Account), GlAccount: am.GLAccount}
		}
		out.Mappings[i] = gm
	}
	if m, ok := b.Current(); ok {
		v := saturate32(m.Version)
		out.CurrentMappingVersion = &v
	}
	return out
}

func toERPExport(v app.ERPBatchView, withLines bool) gen.ERPExport {
	b := v.Batch
	out := gen.ERPExport{
		ID: b.ID.String(), BridgeID: b.Bridge.String(), LegalEntityID: b.LegalEntity.String(), Period: b.Period,
		Currency: string(b.Currency), PostingDate: b.PostingDate.Format(time.DateOnly), CompanyCode: b.CompanyCode,
		MappingVersion: saturate32(b.MappingVersion), ChartVersion: b.ChartVersion, MappingDigest: b.MappingDigest.String(),
		ExternalRef: b.ExternalRef(), JournalIds: make([]string, len(b.Journals)), LineCount: saturate32(len(b.Lines)),
		TotalDebits: b.TotalDebits.CanonicalString(), TotalCredits: b.TotalCredits.CanonicalString(),
		BatchDigest: b.Digest.String(), PreparedAt: canonical.FormatTime(b.PreparedAt), PreparedBy: b.PreparedBy.String(),
		Status: gen.ERPExportStatus(b.Status), History: make([]gen.ERPExportEvent, len(b.History)),
	}
	for i, j := range b.Journals {
		out.JournalIds[i] = j.String()
	}
	for i, e := range b.History {
		ge := gen.ERPExportEvent{
			Seq: saturate32(e.Seq), Kind: gen.ERPExportEventKind(e.Kind), Status: gen.ERPExportStatus(e.Status),
			RemoteCorrelation: optStr(e.RemoteCorrelation), ErpReference: optStr(e.ERPReference),
			ResponseDigest: optStr(e.ResponseDigest), Detail: optStr(e.Detail),
			RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String(),
		}
		if !e.Attempt.IsZero() {
			a := e.Attempt.String()
			ge.AttemptID = &a
		}
		out.History[i] = ge
	}
	if t := v.Correction; t != nil {
		c := gen.ERPCorrectionTask{Open: t.Open(), OpenedAt: canonical.FormatTime(t.OpenedAt), Reason: optStr(t.Reason),
			OutstandingJournalIds: make([]string, len(t.Outstanding)), PostedBy: make([]string, len(t.PostedBy))}
		for i, j := range t.Outstanding {
			c.OutstandingJournalIds[i] = j.String()
		}
		for i, p := range t.PostedBy {
			c.PostedBy[i] = p.String()
		}
		out.Correction = &c
	}
	if withLines {
		out.Lines = make([]gen.ERPExportLine, len(b.Lines))
		for i, l := range b.Lines {
			out.Lines[i] = gen.ERPExportLine{
				JournalID: l.Journal.String(), JournalLine: saturate32(l.JournalLine), Account: gen.ControlAccount(l.Account),
				GlAccount: l.GLAccount, Side: gen.ERPExportLineSide(l.Side), Amount: l.Amount.CanonicalString(),
				Authority: optStr(l.Authority), Jurisdiction: optStr(l.Jurisdiction),
			}
		}
	}
	return out
}

func toGLSnapshot(s erp.GLSnapshot) gen.GLSnapshot {
	out := gen.GLSnapshot{
		ID: s.ID.String(), BridgeID: s.Bridge.String(), Period: s.Period, Currency: string(s.Currency),
		AsOf: canonical.FormatTime(s.AsOf), SourceRef: s.SourceRef, SourceDigest: optStr(s.SourceDigest),
		Digest: s.Digest.String(), RecordedAt: canonical.FormatTime(s.RecordedAt), RecordedBy: s.RecordedBy.String(),
		Balances: make([]gen.GLBalanceLine, len(s.Lines)),
	}
	for i, l := range s.Lines {
		out.Balances[i] = gen.GLBalanceLine{GlAccount: l.GLAccount, Debits: l.Debits.CanonicalString(), Credits: l.Credits.CanonicalString()}
	}
	return out
}

func toGLReconciliation(rec erp.GLReconciliation) gen.GLReconciliation {
	out := gen.GLReconciliation{
		ID: rec.ID.String(), BridgeID: rec.Bridge.String(), SnapshotID: rec.Snapshot.String(),
		SnapshotDigest: rec.SnapshotDigest.String(), LegalEntityID: rec.LegalEntity.String(), Period: rec.Period,
		Currency: string(rec.Currency), MappingVersion: saturate32(rec.MappingVersion), Outcome: gen.GLReconOutcome(rec.Outcome),
		Holds: make([]gen.GLHold, len(rec.Holds)), JournalCount: saturate32(rec.JournalCount),
		PostedJournalCount: saturate32(rec.PostedJournals), PopulationDigest: rec.PopulationDigest.String(),
		ExceptionCount: saturate32(rec.Exceptions()), Items: make([]gen.GLReconciliationItem, len(rec.Items)),
		RanAt: canonical.FormatTime(rec.RanAt),
	}
	if !rec.RanBy.IsZero() {
		by := rec.RanBy.String()
		out.RanBy = &by
	}
	for i, h := range rec.Holds {
		out.Holds[i] = gen.GLHold{Reason: gen.GLHoldReason(h.Reason), Journals: saturate32(h.Journals)}
	}
	for i, it := range rec.Items {
		gi := gen.GLReconciliationItem{
			MatchKey: string(it.Key), GlAccount: optStr(it.GLAccount), Status: gen.GLReconciliationItemStatus(it.Status),
			Detail: optStr(it.Detail), Expected: moneyOut(it.Expected), Observed: moneyOut(it.Observed),
			Variance: moneyOut(it.Variance), TcslAccounts: make([]gen.ControlAccount, len(it.TCSLAccounts)),
		}
		for j, a := range it.TCSLAccounts {
			gi.TcslAccounts[j] = gen.ControlAccount(a)
		}
		out.Items[i] = gi
	}
	return out
}

func (rt *Router) erpBridgeID(w http.ResponseWriter, r *http.Request) (id.ERPBridgeID, bool) {
	b, err := id.ParseERPBridgeID(r.PathValue("bridgeId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("bridgeId", errs.ReasonInvalidValue, "That is not a valid ERP bridge identifier."))
		return id.ERPBridgeID{}, false
	}
	return b, true
}

func (rt *Router) erpBatchIDs(w http.ResponseWriter, r *http.Request) (id.ERPBridgeID, id.ERPExportID, bool) {
	bridge, ok := rt.erpBridgeID(w, r)
	if !ok {
		return id.ERPBridgeID{}, id.ERPExportID{}, false
	}
	batch, err := id.ParseERPExportID(r.PathValue("batchId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("batchId", errs.ReasonInvalidValue, "That is not a valid ERP export batch identifier."))
		return id.ERPBridgeID{}, id.ERPExportID{}, false
	}
	return bridge, batch, true
}

func (rt *Router) handleListERPBridges(w http.ResponseWriter, r *http.Request) {
	if rt.erpUnavailable(w, r) {
		return
	}
	got, err := rt.ERP.Bridges(r.Context())
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.ERPBridgeList{Bridges: make([]gen.ERPBridge, len(got))}
	for i, b := range got {
		out.Bridges[i] = toERPBridge(b)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleCreateERPBridge(w http.ResponseWriter, r *http.Request) {
	if rt.erpUnavailable(w, r) {
		return
	}
	var req gen.ERPBridgeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if !req.Vendor.Valid() {
		writeProblem(w, r, rt.log, errs.Invalid("vendor", errs.ReasonInvalidValue, "That is not an ERP vendor."))
		return
	}
	in := app.ERPBridgeInput{Name: req.Name, Vendor: erp.Vendor(req.Vendor), CompanyCode: req.CompanyCode}
	if req.LegalEntityID != nil {
		le, err := id.ParseLegalEntityID(*req.LegalEntityID)
		if err != nil {
			writeProblem(w, r, rt.log, errs.Invalid("legalEntityId", errs.ReasonInvalidValue, "That is not a valid legal entity identifier."))
			return
		}
		in.LegalEntity = le
	}
	b, err := rt.ERP.CreateBridge(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toERPBridge(b))
}

func (rt *Router) handleRecordERPMapping(w http.ResponseWriter, r *http.Request) {
	if rt.erpUnavailable(w, r) {
		return
	}
	bridge, ok := rt.erpBridgeID(w, r)
	if !ok {
		return
	}
	var req gen.ERPMappingRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in := app.ERPMappingInput{ChartVersion: req.ChartVersion}
	for _, am := range req.Accounts {
		if !am.Account.Valid() {
			writeProblem(w, r, rt.log, errs.Invalid("accounts.account", errs.ReasonInvalidValue, "That is not a TCSL control account."))
			return
		}
		in.Accounts = append(in.Accounts, erp.AccountMap{Account: subledger.Account(am.Account), GLAccount: am.GlAccount})
	}
	b, created, err := rt.ERP.RecordMapping(r.Context(), bridge, in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, r, rt.log, status, toERPBridge(b))
}

func (rt *Router) handleListERPExports(w http.ResponseWriter, r *http.Request) {
	if rt.erpUnavailable(w, r) {
		return
	}
	bridge, ok := rt.erpBridgeID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := erp.Filter{Period: q.Get("period"), Currency: fiscal.Currency(q.Get("currency")), Status: erp.Status(q.Get("status"))}
	switch {
	case f.Status != "" && !f.Status.Valid():
		writeProblem(w, r, rt.log, errs.Invalid("status", errs.ReasonInvalidValue, "That is not an export transfer state."))
		return
	case f.Currency != "" && !erp.ValidCurrency(string(f.Currency)):
		writeProblem(w, r, rt.log, errs.Invalid("currency", errs.ReasonInvalidValue, "The currency is an ISO 4217 code, such as USD."))
		return
	}
	got, err := rt.ERP.Batches(r.Context(), bridge, f)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.ERPExportList{Exports: make([]gen.ERPExport, len(got))}
	for i, v := range got {
		out.Exports[i] = toERPExport(v, false)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handlePrepareERPExport(w http.ResponseWriter, r *http.Request) {
	if rt.erpUnavailable(w, r) {
		return
	}
	bridge, ok := rt.erpBridgeID(w, r)
	if !ok {
		return
	}
	var req gen.ERPExportRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, created, err := rt.ERP.Prepare(r.Context(), bridge, app.ERPPrepareInput{Period: req.Period, Currency: req.Currency, PostingDate: req.PostingDate})
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, r, rt.log, status, toERPExport(v, true))
}

func (rt *Router) handleGetERPExport(w http.ResponseWriter, r *http.Request) {
	if rt.erpUnavailable(w, r) {
		return
	}
	bridge, batch, ok := rt.erpBatchIDs(w, r)
	if !ok {
		return
	}
	v, err := rt.ERP.Batch(r.Context(), bridge, batch)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toERPExport(v, true))
}

func (rt *Router) handleDispatchERPExport(w http.ResponseWriter, r *http.Request) {
	if rt.erpUnavailable(w, r) {
		return
	}
	bridge, batch, ok := rt.erpBatchIDs(w, r)
	if !ok {
		return
	}
	var req gen.ERPDispatchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, dispatched, err := rt.ERP.Dispatch(r.Context(), bridge, batch, req.ExpectedDigest)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if dispatched {
		status = http.StatusAccepted
	}
	writeJSON(w, r, rt.log, status, toERPExport(v, true))
}

func (rt *Router) handleRecordERPResponse(w http.ResponseWriter, r *http.Request) {
	if rt.erpUnavailable(w, r) {
		return
	}
	bridge, batch, ok := rt.erpBatchIDs(w, r)
	if !ok {
		return
	}
	var req gen.ERPResponseRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if !req.Kind.Valid() {
		writeProblem(w, r, rt.log, errs.Invalid("kind", errs.ReasonInvalidValue, "That is not an ERP answer."))
		return
	}
	attempt, err := id.ParseERPAttemptID(req.AttemptID)
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("attemptId", errs.ReasonInvalidValue, "That is not a valid dispatch attempt identifier."))
		return
	}
	v, recorded, err := rt.ERP.Respond(r.Context(), bridge, batch, erp.Response{
		Kind: erp.EventKind(req.Kind), Attempt: attempt, RemoteCorrelation: deref(req.RemoteCorrelation),
		ERPReference: deref(req.ErpReference), ResponseDigest: deref(req.ResponseDigest), Detail: deref(req.Detail),
	})
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if recorded {
		status = http.StatusCreated
	}
	writeJSON(w, r, rt.log, status, toERPExport(v, true))
}

func (rt *Router) handleWithdrawERPExport(w http.ResponseWriter, r *http.Request) {
	if rt.erpUnavailable(w, r) {
		return
	}
	bridge, batch, ok := rt.erpBatchIDs(w, r)
	if !ok {
		return
	}
	var req gen.ERPWithdrawalRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.ERP.Withdraw(r.Context(), bridge, batch, req.Reason)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toERPExport(v, true))
}

func (rt *Router) handleListGLSnapshots(w http.ResponseWriter, r *http.Request) {
	if rt.erpUnavailable(w, r) {
		return
	}
	bridge, ok := rt.erpBridgeID(w, r)
	if !ok {
		return
	}
	got, err := rt.ERP.Snapshots(r.Context(), bridge, r.URL.Query().Get("period"))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.GLSnapshotList{Snapshots: make([]gen.GLSnapshot, len(got))}
	for i, s := range got {
		out.Snapshots[i] = toGLSnapshot(s)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleRecordGLSnapshot(w http.ResponseWriter, r *http.Request) {
	if rt.erpUnavailable(w, r) {
		return
	}
	bridge, ok := rt.erpBridgeID(w, r)
	if !ok {
		return
	}
	var req gen.GLSnapshotRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in := app.GLSnapshotInput{Period: req.Period, Currency: req.Currency, AsOf: req.AsOf, SourceRef: req.SourceRef,
		SourceDigest: deref(req.SourceDigest)}
	for _, b := range req.Balances {
		in.Balances = append(in.Balances, app.GLSnapshotBalance{GLAccount: b.GlAccount, Debits: b.Debits, Credits: b.Credits})
	}
	s, err := rt.ERP.RecordSnapshot(r.Context(), bridge, in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toGLSnapshot(s))
}

func (rt *Router) handleListGLReconciliations(w http.ResponseWriter, r *http.Request) {
	if rt.erpUnavailable(w, r) {
		return
	}
	bridge, ok := rt.erpBridgeID(w, r)
	if !ok {
		return
	}
	got, err := rt.ERP.Reconciliations(r.Context(), bridge, r.URL.Query().Get("period"))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.GLReconciliationList{Reconciliations: make([]gen.GLReconciliation, len(got))}
	for i, rec := range got {
		out.Reconciliations[i] = toGLReconciliation(rec)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleRunGLReconciliation(w http.ResponseWriter, r *http.Request) {
	if rt.erpUnavailable(w, r) {
		return
	}
	bridge, ok := rt.erpBridgeID(w, r)
	if !ok {
		return
	}
	var req gen.GLReconciliationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	snapshot, err := id.ParseGLSnapshotID(req.SnapshotID)
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("snapshotId", errs.ReasonInvalidValue, "That is not a valid GL balance snapshot identifier."))
		return
	}
	rec, err := rt.ERP.Reconcile(r.Context(), bridge, snapshot)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toGLReconciliation(rec))
}
