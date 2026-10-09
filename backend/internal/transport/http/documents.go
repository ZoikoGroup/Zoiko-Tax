package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The fiscal document surface (W2 lane J; ZTAX-FIN-001 §3–§6).

func toDocument(v app.DocumentView) gen.FiscalDocument {
	r, d := v.Record, v.Record.Document
	out := gen.FiscalDocument{
		ID: d.ID.String(), Type: gen.FiscalDocumentType(d.Type), RootID: d.Root.String(),
		Predecessors: make([]gen.DocumentID, len(d.Predecessors)), LegalEntityID: d.LegalEntity.String(),
		IssueDate: d.IssueDate.Format(time.DateOnly), TaxPoint: canonical.FormatTime(d.TaxPoint),
		Currency: string(d.Currency), DecisionIds: make([]gen.DecisionID, len(d.Decisions)),
		Lines: make([]gen.DocumentLine, len(d.Lines)), NetTotal: r.Net.CanonicalString(),
		TaxTotal: r.Tax.CanonicalString(), GrossTotal: r.Gross.CanonicalString(),
		Status: gen.FiscalDocumentStatus(v.Status()), History: make([]gen.DocumentStatusEvent, len(v.History)),
		RecordedAt: canonical.FormatTime(r.RecordedAt),
	}
	for i, p := range d.Predecessors {
		out.Predecessors[i] = p.String()
	}
	for i, dec := range d.Decisions {
		out.DecisionIds[i] = dec.String()
	}
	if d.Number != "" {
		n := d.Number
		out.Number = &n
	}
	if d.Reason != "" {
		reason := gen.ReasonCode(d.Reason)
		out.ReasonCode = &reason
	}
	if d.External != nil {
		out.ExternalRef = &gen.ExternalReference{SourceSystem: d.External.SourceSystem, Namespace: d.External.Namespace, Value: d.External.Value}
	}
	if !r.RecordedBy.IsZero() {
		u := r.RecordedBy.String()
		out.RecordedBy = &u
	}
	for i, l := range d.Lines {
		gl := gen.DocumentLine{ID: l.ID.String(), Net: l.Net.CanonicalString(), Taxes: make([]gen.DocumentTax, len(l.Taxes))}
		if l.SourceLineRef != "" {
			s := l.SourceLineRef
			gl.SourceLineRef = &s
		}
		if l.ComponentInstance != "" {
			s := l.ComponentInstance
			gl.ComponentInstance = &s
		}
		if l.Discount != nil {
			s := l.Discount.CanonicalString()
			gl.Discount = &s
		}
		if l.AllocationRef != "" {
			s := l.AllocationRef
			gl.AllocationRef = &s
		}
		if l.Predecessor != nil {
			s := l.Predecessor.String()
			gl.PredecessorLineID = &s
		}
		for j, t := range l.Taxes {
			gl.Taxes[j] = gen.DocumentTax{DecisionID: t.Decision.String(), Component: t.Component, Amount: t.Amount.CanonicalString()}
		}
		out.Lines[i] = gl
	}
	for i, e := range v.History {
		ge := gen.DocumentStatusEvent{Seq: saturate32(e.Seq), Status: gen.DocumentStatusEventStatus(e.Status), RecordedAt: canonical.FormatTime(e.RecordedAt)}
		if e.Cause != nil {
			s := e.Cause.String()
			ge.CauseID = &s
		}
		if !e.RecordedBy.IsZero() {
			s := e.RecordedBy.String()
			ge.RecordedBy = &s
		}
		out.History[i] = ge
	}
	return out
}

func (rt *Router) documentsUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Documents != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonNoContentBundle,
		"This cell is not configured for determination. The request was not applied."))
	return true
}

func (rt *Router) documentID(w http.ResponseWriter, r *http.Request) (id.FiscalDocumentID, bool) {
	docID, err := id.ParseFiscalDocumentID(r.PathValue("documentId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("documentId", errs.ReasonInvalidValue, "That is not a valid document identifier."))
		return id.FiscalDocumentID{}, false
	}
	return docID, true
}

func parseCivilDate(field, s string) (time.Time, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return time.Time{}, errs.Invalid(field, errs.ReasonInvalidValue, "A date is written YYYY-MM-DD.")
	}
	return t, nil
}

func amountIn(field, v string, cur fiscal.Currency) (fiscal.Money, error) {
	return parseMoney(field, gen.MoneyValue{Amount: v, Currency: string(cur)})
}

// documentLines parses presented lines in the document's currency.
func documentLines(prefix string, in []gen.DocumentLineRequest, cur fiscal.Currency) ([]app.LineInput, error) {
	out := make([]app.LineInput, len(in))
	for i, l := range in {
		f := fmt.Sprintf("%s[%d]", prefix, i)
		net, err := amountIn(f+".net", l.Net, cur)
		if err != nil {
			return nil, err
		}
		li := app.LineInput{Net: net, Taxes: make([]document.TaxLine, len(l.Taxes))}
		if l.SourceLineRef != nil {
			li.SourceLineRef = *l.SourceLineRef
		}
		if l.ComponentInstance != nil {
			li.ComponentInstance = *l.ComponentInstance
		}
		if l.AllocationRef != nil {
			li.AllocationRef = *l.AllocationRef
		}
		if l.Discount != nil {
			d, err := amountIn(f+".discount", *l.Discount, cur)
			if err != nil {
				return nil, err
			}
			li.Discount = &d
		}
		if l.PredecessorLineID != nil {
			p, err := id.ParseFiscalLineID(*l.PredecessorLineID)
			if err != nil {
				return nil, errs.Invalid(f+".predecessorLineId", errs.ReasonInvalidValue, "That is not a valid line identifier.")
			}
			li.Predecessor = &p
		}
		for j, t := range l.Taxes {
			tf := fmt.Sprintf("%s.taxes[%d]", f, j)
			dec, err := id.ParseDecisionID(t.DecisionID)
			if err != nil {
				return nil, errs.Invalid(tf+".decisionId", errs.ReasonInvalidValue, "That is not a valid decision identifier.")
			}
			if strings.TrimSpace(t.Component) == "" {
				return nil, errs.Invalid(tf+".component", errs.ReasonMissingField, "A tax names its component.")
			}
			amt, err := amountIn(tf+".amount", t.Amount, cur)
			if err != nil {
				return nil, err
			}
			li.Taxes[j] = document.TaxLine{Decision: dec, Component: t.Component, Amount: amt}
		}
		out[i] = li
	}
	return out, nil
}

// settle writes an idempotent document write's settled response.
func (rt *Router) settle(w http.ResponseWriter, settled app.Settled) {
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

func (rt *Router) renderers(r *http.Request) (func(app.DocumentView) ([]byte, error), func(error) app.Response) {
	return func(v app.DocumentView) ([]byte, error) {
			body, err := json.Marshal(toDocument(v))
			if err != nil {
				return nil, err
			}
			return append(body, '\n'), nil
		}, func(err error) app.Response {
			status, body, _ := renderProblem(r, rt.log, err)
			return app.Response{Status: status, Body: body}
		}
}

func (rt *Router) idempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeProblem(w, r, rt.log, errs.New(errs.CategoryValidation, errs.ReasonIdempotencyKeyRequired,
			"This endpoint requires an Idempotency-Key header. The request was not applied."))
		return "", false
	}
	return key, true
}

// handleIssueDocument is POST /v1/documents.
func (rt *Router) handleIssueDocument(w http.ResponseWriter, r *http.Request) {
	if rt.documentsUnavailable(w, r) {
		return
	}
	key, ok := rt.idempotencyKey(w, r)
	if !ok {
		return
	}
	var req gen.DocumentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in, err := issueInput(req)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in.IdempotencyKey = key
	in.Render, in.RenderFailure = rt.renderers(r)
	settled, err := rt.Documents.Issue(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	rt.settle(w, settled)
}

func issueInput(req gen.DocumentRequest) (app.IssueInput, error) {
	if !currencyForm.MatchString(req.Currency) {
		return app.IssueInput{}, errs.Invalid("currency", errs.ReasonInvalidValue, "A currency is an ISO 4217 alphabetic code, such as EUR.")
	}
	cur := fiscal.Currency(req.Currency)
	issue, err := parseCivilDate("issueDate", req.IssueDate)
	if err != nil {
		return app.IssueInput{}, err
	}
	taxPoint, err := parseTimestamp("taxPoint", req.TaxPoint)
	if err != nil {
		return app.IssueInput{}, err
	}
	lines, err := documentLines("lines", req.Lines, cur)
	if err != nil {
		return app.IssueInput{}, err
	}
	in := app.IssueInput{Type: document.Type(req.Type), IssueDate: issue, TaxPoint: taxPoint, Currency: cur, Lines: lines}
	if req.Number != nil {
		in.Number = *req.Number
	}
	if req.ExternalRef != nil {
		ext := id.ExternalReference{SourceSystem: req.ExternalRef.SourceSystem, Namespace: req.ExternalRef.Namespace, Value: req.ExternalRef.Value}
		if err := ext.Validate(); err != nil {
			return app.IssueInput{}, errs.Invalid("externalRef", errs.ReasonInvalidValue, "An external reference names its source system, namespace and value.")
		}
		in.External = &ext
	}
	return in, nil
}

// handleCorrectDocument is POST /v1/documents/{documentId}/corrections.
func (rt *Router) handleCorrectDocument(w http.ResponseWriter, r *http.Request) {
	if rt.documentsUnavailable(w, r) {
		return
	}
	docID, ok := rt.documentID(w, r)
	if !ok {
		return
	}
	key, ok := rt.idempotencyKey(w, r)
	if !ok {
		return
	}
	var req gen.CorrectionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	issue, err := parseCivilDate("issueDate", req.IssueDate)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	taxPoint, err := parseTimestamp("taxPoint", req.TaxPoint)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in := app.CorrectionInput{
		IdempotencyKey: key, Original: docID, Type: document.Type(req.Type), Reason: errs.ReasonCode(req.Reason),
		IssueDate: issue, TaxPoint: taxPoint,
	}
	if req.Number != nil {
		in.Number = *req.Number
	}
	for i, l := range req.CancelLines {
		lid, err := id.ParseFiscalLineID(l)
		if err != nil {
			writeProblem(w, r, rt.log, errs.Invalid(fmt.Sprintf("cancelLines[%d]", i), errs.ReasonInvalidValue, "That is not a valid line identifier."))
			return
		}
		in.CancelLines = append(in.CancelLines, lid)
	}
	if len(req.Lines) > 0 {
		// A rebill's lines are in the original's currency, which the
		// service checks against the decisions they cite; the original is
		// read for it here only to parse amounts at the right currency.
		orig, err := rt.Documents.Document(r.Context(), docID)
		if err != nil {
			writeProblem(w, r, rt.log, err)
			return
		}
		if in.Lines, err = documentLines("lines", req.Lines, orig.Record.Document.Currency); err != nil {
			writeProblem(w, r, rt.log, err)
			return
		}
	}
	in.Render, in.RenderFailure = rt.renderers(r)
	settled, err := rt.Documents.Correct(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	rt.settle(w, settled)
}

func (rt *Router) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	if rt.documentsUnavailable(w, r) {
		return
	}
	docID, ok := rt.documentID(w, r)
	if !ok {
		return
	}
	v, err := rt.Documents.Document(r.Context(), docID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toDocument(v))
}

func (rt *Router) handleDocumentLineage(w http.ResponseWriter, r *http.Request) {
	if rt.documentsUnavailable(w, r) {
		return
	}
	docID, ok := rt.documentID(w, r)
	if !ok {
		return
	}
	chain, err := rt.Documents.Lineage(r.Context(), docID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.DocumentLineage{Documents: make([]gen.FiscalDocument, len(chain))}
	for i, v := range chain {
		out.Documents[i] = toDocument(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}
