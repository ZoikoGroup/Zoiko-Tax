package http

import (
	"net/http"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/remittance"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The remittance surface (ZTAX-REM-001 §34, W4 lane M).

func (rt *Router) remittanceUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Remittance != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"Remittance is not wired in this cell. The request was not applied."))
	return true
}

func toBeneficiary(b remittance.Beneficiary) gen.Beneficiary {
	out := gen.Beneficiary{
		ID: b.ID.String(), Country: b.Country, Authority: b.Authority, LegalName: b.LegalName,
		Rail: gen.PaymentRail(b.Rail), AccountRef: b.AccountRef, ReferenceRule: optStr(b.ReferenceRule),
		Currencies: append([]string{}, b.Currencies...), Source: b.Source, State: gen.BeneficiaryState(b.State),
		CreatedAt: canonical.FormatTime(b.CreatedAt), CreatedBy: b.CreatedBy.String(),
		History: make([]gen.BeneficiaryEvent, len(b.History)),
	}
	if !b.Supersedes.IsZero() {
		s := b.Supersedes.String()
		out.Supersedes = &s
	}
	for i, e := range b.History {
		out.History[i] = gen.BeneficiaryEvent{Seq: saturate32(e.Seq), Kind: gen.BeneficiaryEventKind(e.Kind),
			Evidence: optStr(e.Evidence), RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String()}
	}
	return out
}

func toRemittance(v app.RemittanceView) gen.Remittance {
	rem := v.Remittance
	out := gen.Remittance{
		ID: rem.ID.String(), ObligationID: rem.Obligation.String(), ObligationStatus: gen.ObligationStatus(v.ObligationStatus),
		LegalEntityID: rem.LegalEntity.String(), Authority: rem.Authority,
		// Civil dates in the legal calendar; formatted there, never in UTC.
		PeriodStart: rem.PeriodStart.Format(time.DateOnly), PeriodEnd: rem.PeriodEnd.Format(time.DateOnly),
		PaymentDue: canonical.FormatTime(rem.PaymentDue), Timezone: rem.Timezone,
		Amount: rem.Amount.CanonicalString(), Currency: string(rem.Amount.Currency()), Beneficiary: toBeneficiary(v.Beneficiary),
		FundingRef: rem.FundingRef, PaymentReference: rem.PaymentReference, ExecutionDate: rem.ExecutionDate.Format(time.DateOnly),
		Mode: gen.RemittanceMode(rem.Mode), FundsPosture: gen.RemittanceFundsPosture(app.FundsPosture),
		InstructionHash: rem.InstructionHash.String(), Status: gen.RemittanceStatus(rem.Status), Overdue: v.Overdue,
		Ready: v.Ready, Readiness: make([]gen.RemittanceReadiness, len(v.Readiness)),
		CreatedAt: canonical.FormatTime(rem.CreatedAt), CreatedBy: rem.CreatedBy.String(),
		History: make([]gen.RemittanceStep, len(rem.History)),
	}
	if !rem.Supersedes.IsZero() {
		s := rem.Supersedes.String()
		out.Supersedes = &s
	}
	for i, c := range v.Readiness {
		out.Readiness[i] = gen.RemittanceReadiness{Dimension: gen.RemittanceReadinessDimension(c.Dimension), Ready: c.Ready, Detail: c.Detail}
	}
	for i, s := range rem.History {
		st := gen.RemittanceStep{
			Seq: saturate32(s.Seq), Kind: gen.RemittanceStepKind(s.Kind), Status: gen.RemittanceStatus(s.Status),
			ProviderReference: optStr(s.ProviderRef), Raw: optStr(s.Raw), Note: optStr(s.Note),
			RecordedAt: canonical.FormatTime(s.RecordedAt), RecordedBy: s.RecordedBy.String(),
		}
		if !s.InstructionHash.IsZero() {
			h := s.InstructionHash.String()
			st.InstructionHash = &h
		}
		if !s.Submission.IsZero() {
			a := s.Submission.String()
			st.SubmissionID = &a
		}
		if s.Evidence != "" {
			e := gen.PaymentEvidence(s.Evidence)
			st.Evidence = &e
		}
		if s.Resolution != "" {
			r := gen.PaymentResolution(s.Resolution)
			st.Resolution = &r
		}
		out.History[i] = st
	}
	return out
}

func (rt *Router) beneficiaryID(w http.ResponseWriter, r *http.Request) (id.BeneficiaryID, bool) {
	beneficiaryID, err := id.ParseBeneficiaryID(r.PathValue("beneficiaryId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("beneficiaryId", errs.ReasonInvalidValue, "That is not a valid beneficiary identifier."))
		return id.BeneficiaryID{}, false
	}
	return beneficiaryID, true
}

func (rt *Router) remittanceID(w http.ResponseWriter, r *http.Request) (id.RemittanceID, bool) {
	remittanceID, err := id.ParseRemittanceID(r.PathValue("remittanceId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("remittanceId", errs.ReasonInvalidValue, "That is not a valid payment identifier."))
		return id.RemittanceID{}, false
	}
	return remittanceID, true
}

func (rt *Router) handleListBeneficiaries(w http.ResponseWriter, r *http.Request) {
	if rt.remittanceUnavailable(w, r) {
		return
	}
	got, err := rt.Remittance.Beneficiaries(r.Context())
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.BeneficiaryList{Beneficiaries: make([]gen.Beneficiary, len(got))}
	for i, b := range got {
		out.Beneficiaries[i] = toBeneficiary(b)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleCreateBeneficiary(w http.ResponseWriter, r *http.Request) {
	if rt.remittanceUnavailable(w, r) {
		return
	}
	var req gen.BeneficiaryRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if !req.Rail.Valid() {
		writeProblem(w, r, rt.log, errs.Invalid("rail", errs.ReasonInvalidValue, "That is not a payment rail this service knows."))
		return
	}
	in := app.BeneficiaryInput{Country: req.Country, Authority: req.Authority, LegalName: req.LegalName,
		Rail: remittance.Rail(req.Rail), AccountRef: req.AccountRef, ReferenceRule: deref(req.ReferenceRule),
		Currencies: req.Currencies, Source: req.Source}
	if req.Supersedes != nil {
		sup, err := id.ParseBeneficiaryID(*req.Supersedes)
		if err != nil {
			writeProblem(w, r, rt.log, errs.Invalid("supersedes", errs.ReasonInvalidValue, "That is not a valid beneficiary identifier."))
			return
		}
		in.Supersedes = sup
	}
	b, err := rt.Remittance.CreateBeneficiary(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toBeneficiary(b))
}

func (rt *Router) handleVerifyBeneficiary(w http.ResponseWriter, r *http.Request) {
	if rt.remittanceUnavailable(w, r) {
		return
	}
	beneficiaryID, ok := rt.beneficiaryID(w, r)
	if !ok {
		return
	}
	var req gen.BeneficiaryVerificationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	b, err := rt.Remittance.VerifyBeneficiary(r.Context(), beneficiaryID, req.Evidence)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toBeneficiary(b))
}

func (rt *Router) handleSuspendBeneficiary(w http.ResponseWriter, r *http.Request) {
	if rt.remittanceUnavailable(w, r) {
		return
	}
	beneficiaryID, ok := rt.beneficiaryID(w, r)
	if !ok {
		return
	}
	var req gen.BeneficiarySuspensionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	b, err := rt.Remittance.SuspendBeneficiary(r.Context(), beneficiaryID, req.Reason)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toBeneficiary(b))
}

func (rt *Router) handleListRemittances(w http.ResponseWriter, r *http.Request) {
	if rt.remittanceUnavailable(w, r) {
		return
	}
	status := remittance.Status(r.URL.Query().Get("status"))
	if status != "" && !gen.RemittanceStatus(status).Valid() {
		writeProblem(w, r, rt.log, errs.Invalid("status", errs.ReasonInvalidValue, "That is not a payment status."))
		return
	}
	views, err := rt.Remittance.Remittances(r.Context(), status, limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.RemittanceList{Remittances: make([]gen.Remittance, len(views))}
	for i, v := range views {
		out.Remittances[i] = toRemittance(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleCreateRemittance(w http.ResponseWriter, r *http.Request) {
	if rt.remittanceUnavailable(w, r) {
		return
	}
	var req gen.RemittanceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	obligationID, err := id.ParseObligationID(req.ObligationID)
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("obligationId", errs.ReasonInvalidValue, "That is not a valid obligation identifier."))
		return
	}
	beneficiaryID, err := id.ParseBeneficiaryID(req.BeneficiaryID)
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("beneficiaryId", errs.ReasonInvalidValue, "That is not a valid beneficiary identifier."))
		return
	}
	execution, err := time.Parse(time.DateOnly, req.ExecutionDate)
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("executionDate", errs.ReasonInvalidValue, "The execution date is a calendar date, YYYY-MM-DD."))
		return
	}
	if !req.Mode.Valid() {
		writeProblem(w, r, rt.log, errs.Invalid("mode", errs.ReasonInvalidValue, "That is not an operating model this service knows."))
		return
	}
	v, created, err := rt.Remittance.CreateRemittance(r.Context(), app.RemittanceInput{
		Obligation: obligationID, Beneficiary: beneficiaryID, FundingRef: req.FundingRef,
		PaymentReference: req.PaymentReference, ExecutionDate: execution, Mode: remittance.Mode(req.Mode),
	})
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, r, rt.log, status, toRemittance(v))
}

func (rt *Router) handleGetRemittance(w http.ResponseWriter, r *http.Request) {
	if rt.remittanceUnavailable(w, r) {
		return
	}
	remittanceID, ok := rt.remittanceID(w, r)
	if !ok {
		return
	}
	v, err := rt.Remittance.Remittance(r.Context(), remittanceID)
	rt.writeRemittance(w, r, v, err)
}

func (rt *Router) handleApproveRemittance(w http.ResponseWriter, r *http.Request) {
	if rt.remittanceUnavailable(w, r) {
		return
	}
	remittanceID, ok := rt.remittanceID(w, r)
	if !ok {
		return
	}
	var req gen.RemittanceApprovalRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	hash, err := canonical.ParseDigest(req.InstructionHash)
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("instructionHash", errs.ReasonInvalidValue, "That is not a canonical digest."))
		return
	}
	v, err := rt.Remittance.ApproveRemittance(r.Context(), remittanceID, hash, deref(req.Note))
	rt.writeRemittance(w, r, v, err)
}

func (rt *Router) handleSubmitRemittance(w http.ResponseWriter, r *http.Request) {
	if rt.remittanceUnavailable(w, r) {
		return
	}
	remittanceID, ok := rt.remittanceID(w, r)
	if !ok {
		return
	}
	var req gen.RemittanceSubmissionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Remittance.SubmitRemittance(r.Context(), remittanceID, deref(req.Note))
	rt.writeRemittance(w, r, v, err)
}

func (rt *Router) handleRecordRemittanceStatus(w http.ResponseWriter, r *http.Request) {
	if rt.remittanceUnavailable(w, r) {
		return
	}
	remittanceID, ok := rt.remittanceID(w, r)
	if !ok {
		return
	}
	var req gen.RemittanceStatusRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Remittance.RecordStatus(r.Context(), remittanceID, app.StatusInput{
		Status: remittance.Status(req.Status), Evidence: remittance.Evidence(req.Evidence),
		ProviderRef: deref(req.ProviderReference), Raw: req.Raw, Note: deref(req.Note),
	})
	rt.writeRemittance(w, r, v, err)
}

func (rt *Router) handleResolveRemittance(w http.ResponseWriter, r *http.Request) {
	if rt.remittanceUnavailable(w, r) {
		return
	}
	remittanceID, ok := rt.remittanceID(w, r)
	if !ok {
		return
	}
	var req gen.RemittanceResolutionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Remittance.ResolveRemittance(r.Context(), remittanceID, remittance.Resolution(req.Resolution),
		remittance.Evidence(req.Evidence), deref(req.ProviderReference), req.Raw)
	rt.writeRemittance(w, r, v, err)
}

func (rt *Router) handleReturnRemittance(w http.ResponseWriter, r *http.Request) {
	if rt.remittanceUnavailable(w, r) {
		return
	}
	remittanceID, ok := rt.remittanceID(w, r)
	if !ok {
		return
	}
	var req gen.RemittanceReturnRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Remittance.ReturnRemittance(r.Context(), remittanceID, deref(req.ProviderReference), req.Raw)
	rt.writeRemittance(w, r, v, err)
}

func (rt *Router) handleCancelRemittance(w http.ResponseWriter, r *http.Request) {
	if rt.remittanceUnavailable(w, r) {
		return
	}
	remittanceID, ok := rt.remittanceID(w, r)
	if !ok {
		return
	}
	var req gen.RemittanceCancellationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Remittance.CancelRemittance(r.Context(), remittanceID, req.Reason)
	rt.writeRemittance(w, r, v, err)
}

func (rt *Router) writeRemittance(w http.ResponseWriter, r *http.Request, v app.RemittanceView, err error) {
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toRemittance(v))
}
