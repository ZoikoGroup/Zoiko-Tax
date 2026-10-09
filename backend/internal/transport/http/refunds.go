package http

import (
	"encoding/json"
	"net/http"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/settlement"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The refund surface (ZTAX-FIN-001 §14; W2 lane K).

func toRefund(v app.RefundView) gen.Refund {
	rf := v.Refund
	out := gen.Refund{
		ID: rf.ID.String(), DecisionID: rf.Decision.String(), LegalEntityID: rf.LegalEntity.String(),
		Amount:           gen.MoneyValue{Amount: rf.Amount.CanonicalString(), Currency: string(rf.Amount.Currency())},
		PaymentReference: rf.PaymentRef,
		Status:           gen.RefundStatus(v.Current().Status),
		RequestedAt:      canonical.FormatTime(rf.RequestedAt),
		History:          make([]gen.RefundEvent, len(v.History)),
	}
	if rf.Reason != "" {
		reason := rf.Reason
		out.Reason = &reason
	}
	if !rf.RequestedBy.IsZero() {
		u := rf.RequestedBy.String()
		out.RequestedBy = &u
	}
	for i, e := range v.History {
		ge := gen.RefundEvent{
			Seq: saturate32(e.Seq), Status: gen.RefundStatus(e.Status), RecordedAt: canonical.FormatTime(e.RecordedAt),
		}
		if e.Outcome != "" {
			o := gen.RefundOutcome(e.Outcome)
			ge.Outcome = &o
		}
		if e.ExternalRef != "" {
			ref := e.ExternalRef
			ge.ExternalReference = &ref
		}
		if !e.RecordedBy.IsZero() {
			u := e.RecordedBy.String()
			ge.RecordedBy = &u
		}
		out.History[i] = ge
	}
	return out
}

func (rt *Router) refundsUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Refunds != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"This cell is not configured for refunds. The request was not applied."))
	return true
}

func (rt *Router) refundID(w http.ResponseWriter, r *http.Request) (id.RefundID, bool) {
	refundID, err := id.ParseRefundID(r.PathValue("refundId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("refundId", errs.ReasonInvalidValue,
			"That is not a valid refund identifier."))
		return id.RefundID{}, false
	}
	return refundID, true
}

// handleRefund is POST /v1/transactions:refund.
func (rt *Router) handleRefund(w http.ResponseWriter, r *http.Request) {
	if rt.refundsUnavailable(w, r) {
		return
	}
	// Refused before the body is read, as on :commit (ADR-0013 §2.1).
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeProblem(w, r, rt.log, errs.New(errs.CategoryValidation, errs.ReasonIdempotencyKeyRequired,
			"This endpoint requires an Idempotency-Key header. The request was not applied."))
		return
	}
	var req gen.RefundRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	decisionID, err := id.ParseDecisionID(req.DecisionID)
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("decisionId", errs.ReasonInvalidValue,
			"That is not a valid decision identifier."))
		return
	}
	amount, err := parseMoney("amount", req.Amount)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in := app.RefundInput{
		IdempotencyKey: key, Decision: decisionID, Amount: amount, PaymentRef: req.PaymentReference,
		Render: func(v app.RefundView) ([]byte, error) {
			body, err := json.Marshal(toRefund(v))
			if err != nil {
				return nil, err
			}
			return append(body, '\n'), nil
		},
		RenderFailure: func(err error) app.Response {
			status, body, _ := renderProblem(r, rt.log, err)
			return app.Response{Status: status, Body: body}
		},
	}
	if req.Reason != nil {
		in.Reason = *req.Reason
	}
	settled, err := rt.Refunds.Request(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if settled.Replayed {
		w.Header().Set("Idempotent-Replay", "true")
	}
	if settled.Status >= 400 {
		writeProblemBytes(w, settled.Status, settled.Body, "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(settled.Status)
	_, _ = w.Write(settled.Body)
}

func (rt *Router) handleGetRefund(w http.ResponseWriter, r *http.Request) {
	if rt.refundsUnavailable(w, r) {
		return
	}
	refundID, ok := rt.refundID(w, r)
	if !ok {
		return
	}
	v, err := rt.Refunds.Refund(r.Context(), refundID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toRefund(v))
}

func (rt *Router) handleReportRefund(w http.ResponseWriter, r *http.Request) {
	if rt.refundsUnavailable(w, r) {
		return
	}
	refundID, ok := rt.refundID(w, r)
	if !ok {
		return
	}
	var req gen.RefundReportRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in := app.ReportInput{Outcome: settlement.RefundOutcome(req.Outcome)}
	if req.ExternalReference != nil {
		in.ExternalRef = *req.ExternalReference
	}
	v, err := rt.Refunds.Report(r.Context(), refundID, in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toRefund(v))
}
