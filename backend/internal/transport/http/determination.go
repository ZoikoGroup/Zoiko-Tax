package http

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The determination surface: quote, commit, read and replay (W2 lane K).
//
// What stays in this file is the boundary: parsing the contract's strings into
// fiscal values, and rendering decisions back into the contract's shapes.
// Nothing here evaluates, rounds or compares an amount.

// ---------------------------------------------------------------------------
// ingress
// ---------------------------------------------------------------------------

// decimalForm is the contract's Decimal pattern. apd accepts more — exponents,
// a leading "+", "Infinity" — and every one of those is a second spelling of a
// value, which is a second digest for one request (ADR-0011 P1).
var decimalForm = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

var currencyForm = regexp.MustCompile(`^[A-Z]{3}$`)

// timestampLayout is the contract's Timestamp: six fractional digits and a
// literal Z, the form canonical.FormatTime writes. Any other spelling of an
// instant is refused rather than normalised, so that what the caller sent is
// what the envelope records.
const timestampLayout = "2006-01-02T15:04:05.000000Z"

func parseTimestamp(field, s string) (time.Time, error) {
	t, err := time.Parse(timestampLayout, s)
	if err != nil {
		return time.Time{}, errs.Invalid(field, errs.ReasonInvalidValue,
			"An instant is written as UTC with exactly six fractional digits and a trailing Z, such as 2026-09-24T18:00:00.000000Z.")
	}
	return t, nil
}

func parseDecimal(field, s string) error {
	if len(s) > 64 || !decimalForm.MatchString(s) {
		return errs.Invalid(field, errs.ReasonInvalidValue,
			"A decimal is a string of digits with an optional minus and an optional fraction, such as \"45.00\". No exponent, no leading plus, no leading zeros.")
	}
	return nil
}

func parseMoney(field string, v gen.MoneyValue) (fiscal.Money, error) {
	if err := parseDecimal(field+".amount", v.Amount); err != nil {
		return fiscal.Money{}, err
	}
	if !currencyForm.MatchString(v.Currency) {
		return fiscal.Money{}, errs.Invalid(field+".currency", errs.ReasonInvalidValue,
			"A currency is an ISO 4217 alphabetic code, such as EUR.")
	}
	m, err := fiscal.ParseMoney(v.Amount, fiscal.Currency(v.Currency))
	if err != nil {
		return fiscal.Money{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue,
			"The amount cannot be represented at the runtime's precision.")
	}
	return m, nil
}

func parseMoneyMap(field string, in map[string]gen.MoneyValue) (map[string]fiscal.Money, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]fiscal.Money, len(in))
	for name, v := range in {
		m, err := parseMoney(field, v)
		if err != nil {
			return nil, err
		}
		out[name] = m
	}
	return out, nil
}

// determinationInput reads the contract's input into the canonical one. The
// value names are the pack's and pass through untouched; the values are
// parsed, and a value in any other form is refused.
func determinationInput(in gen.DeterminationInput) (evidence.Input, error) {
	money, err := parseMoneyMap("input.money", in.Money)
	if err != nil {
		return evidence.Input{}, err
	}
	out := evidence.Input{Money: money}
	if len(in.Rates) > 0 {
		out.Rates = make(map[string]fiscal.Rate, len(in.Rates))
		for name, v := range in.Rates {
			if err := parseDecimal("input.rates.value", v.Value); err != nil {
				return evidence.Input{}, err
			}
			r, err := fiscal.ParseRate(v.Value, fiscal.RateBasis(v.Basis))
			if err != nil {
				return evidence.Input{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue,
					"The rate or its basis is not one the runtime accepts.")
			}
			out.Rates[name] = r
		}
	}
	if len(in.Quantities) > 0 {
		out.Quantities = make(map[string]fiscal.Quantity, len(in.Quantities))
		for name, v := range in.Quantities {
			if err := parseDecimal("input.quantities.value", v.Value); err != nil {
				return evidence.Input{}, err
			}
			if strings.TrimSpace(v.Unit) == "" || len(v.Unit) > 32 {
				return evidence.Input{}, errs.Invalid("input.quantities.unit", errs.ReasonInvalidValue,
					"A quantity names its unit of measure.")
			}
			q, err := fiscal.ParseQuantity(v.Value, fiscal.Unit(v.Unit))
			if err != nil {
				return evidence.Input{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue,
					"The quantity cannot be represented at the runtime's precision.")
			}
			out.Quantities[name] = q
		}
	}
	if len(in.Flags) > 0 {
		out.Flags = in.Flags
	}
	if len(in.Strings) > 0 {
		out.Strings = in.Strings
	}
	if err := out.Validate(); err != nil {
		return evidence.Input{}, errs.Invalid("input", errs.ReasonMissingField,
			"A determination carries at least one named input value, and every value has a name.")
	}
	return out, nil
}

func readSet(in *gen.ReadSet) (map[string]fiscal.Money, error) {
	if in == nil {
		return nil, nil
	}
	return parseMoneyMap("accumulators", *in)
}

// ---------------------------------------------------------------------------
// egress
// ---------------------------------------------------------------------------

func toResultValue(v rule.Value) gen.ResultValue {
	out := gen.ResultValue{Type: gen.ValueType(v.Type)}
	str := func(s string) *string { return &s }
	switch v.Type {
	case rule.TypeMoney:
		out.Amount, out.Currency = str(v.Money.CanonicalString()), str(string(v.Money.Currency()))
	case rule.TypeRate:
		basis := gen.RateBasis(v.Rate.Basis())
		out.Value, out.Basis = str(v.Rate.CanonicalString()), &basis
	case rule.TypeQuantity:
		out.Value, out.Unit = str(v.Quantity.CanonicalString()), str(string(v.Quantity.Unit()))
	case rule.TypeBool:
		out.Flag = &v.Bool
	case rule.TypeString:
		out.Text = str(v.String)
	case rule.TypeReason:
		out.ReasonCode = str(string(v.Reason))
	}
	return out
}

func toEmitted(values map[string]rule.Value) gen.Emitted {
	out := make(gen.Emitted, len(values))
	for slot, v := range values {
		out[slot] = toResultValue(v)
	}
	return out
}

func toDecision(rec evidence.Record, result evidence.Result) gen.Decision {
	d := gen.Decision{
		ID:          rec.DecisionID.String(),
		BusinessKey: rec.BusinessKey,
		// Filable is false for every outcome this runtime can produce. Asking
		// the outcome rather than writing false keeps the answer where the
		// A4 gate will change it (ZTAX-DET-REQ-0038).
		Authoritative: rec.Outcome.Filable(),
		Outcome:       gen.Outcome(rec.Outcome),
		ReasonCode:    string(rec.Reason),
		EventTime:     canonical.FormatTime(rec.EventTime),
		RecordedAt:    canonical.FormatTime(rec.RecordedAt),
		Bundle: gen.BundleRef{
			BundleID: rec.BundleID, Digest: rec.BundleDigest, IrVersion: saturate32(rec.IRVersion),
		},
		Digests: gen.DecisionDigests{
			Input: rec.InputDigest.String(), Envelope: rec.EnvelopeDigest.String(), Result: rec.ResultDigest.String(),
		},
		Emitted: toEmitted(result.Emitted),
	}
	if rec.Supersedes != nil {
		s := rec.Supersedes.String()
		d.Supersedes = &s
	}
	return d
}

// ---------------------------------------------------------------------------
// handlers
// ---------------------------------------------------------------------------

// determinationUnavailable is the answer of a cell deployed without an
// evidence store: it cannot record a decision, so it serves none of this
// surface rather than the parts that happen not to write.
func (rt *Router) determinationUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Determination != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonNoContentBundle,
		"This cell is not configured for determination. The request was not applied."))
	return true
}

func (rt *Router) handleQuote(w http.ResponseWriter, r *http.Request) {
	if rt.determinationUnavailable(w, r) {
		return
	}
	var req gen.QuoteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	eventTime, err := parseTimestamp("eventTime", req.EventTime)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	input, err := determinationInput(req.Input)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	accumulators, err := readSet(req.Accumulators)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}

	q, err := rt.Determination.Quote(r.Context(), app.QuoteInput{
		EventTime: eventTime, Input: input, Accumulators: accumulators,
	})
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, gen.Quote{
		// A quote is an estimate at every authorization level (ADR-0004 §2.7),
		// so this one is written rather than asked.
		Authoritative: false,
		Outcome:       gen.Outcome(q.Result.Outcome),
		ReasonCode:    string(q.Result.Reason),
		QuotedAt:      canonical.FormatTime(q.QuotedAt),
		Bundle:        gen.BundleRef{BundleID: q.BundleID, Digest: q.BundleDigest, IrVersion: saturate32(q.IRVersion)},
		Emitted:       toEmitted(q.Result.Emitted),
	})
}

func (rt *Router) handleCommit(w http.ResponseWriter, r *http.Request) {
	rt.recordDecision(w, r, rt.Determination.Commit)
}

// handleAdjust is POST /v1/transactions:adjust: a correction evaluated under
// the content bundle that made the decision it supersedes
// (ZTAX-DET-REQ-0030). It shares commit's body, idempotency and rendering;
// the service is what differs.
func (rt *Router) handleAdjust(w http.ResponseWriter, r *http.Request) {
	rt.recordDecision(w, r, rt.Determination.Adjust)
}

// recordDecision is the shared shape of commit and adjust: an idempotent
// request whose success is a recorded decision.
func (rt *Router) recordDecision(w http.ResponseWriter, r *http.Request, record func(context.Context, app.CommitInput) (app.Settled, error)) {
	if rt.determinationUnavailable(w, r) {
		return
	}
	// Refused before the body is read: ADR-0013 §2.1 rejects a keyless commit
	// before any work begins, and a malformed body is still work.
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeProblem(w, r, rt.log, errs.New(errs.CategoryValidation, errs.ReasonIdempotencyKeyRequired,
			"This endpoint requires an Idempotency-Key header. The request was not applied."))
		return
	}
	var req gen.CommitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in, err := commitInput(req)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}

	settled, err := record(r.Context(), app.CommitInput{
		IdempotencyKey: key,
		Determination:  in,
		Render: func(d evidence.Decision) ([]byte, error) {
			rec, err := evidence.NewRecord(d)
			if err != nil {
				return nil, err
			}
			body, err := json.Marshal(toDecision(rec, d.Result))
			if err != nil {
				return nil, err
			}
			// The newline writeJSON's encoder adds, so a committed response and
			// every other response end the same way.
			return append(body, '\n'), nil
		},
		RenderFailure: func(err error) app.Response {
			status, body, _ := renderProblem(r, rt.log, err)
			return app.Response{Status: status, Body: body}
		},
	})
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

func commitInput(req gen.CommitRequest) (app.DetermineInput, error) {
	eventTime, err := parseTimestamp("eventTime", req.EventTime)
	if err != nil {
		return app.DetermineInput{}, err
	}
	if strings.TrimSpace(req.BusinessKey) == "" || len(req.BusinessKey) > 255 {
		return app.DetermineInput{}, errs.Invalid("businessKey", errs.ReasonMissingField,
			"A business key of at most 255 characters is required.")
	}
	input, err := determinationInput(req.Input)
	if err != nil {
		return app.DetermineInput{}, err
	}
	accumulators, err := readSet(req.Accumulators)
	if err != nil {
		return app.DetermineInput{}, err
	}
	in := app.DetermineInput{
		BusinessKey: req.BusinessKey, EventTime: eventTime, Input: input, Accumulators: accumulators,
	}
	if req.Supersedes != nil {
		prior, err := id.ParseDecisionID(*req.Supersedes)
		if err != nil {
			return app.DetermineInput{}, errs.Invalid("supersedes", errs.ReasonInvalidValue,
				"That is not a valid decision identifier.")
		}
		in.Supersedes = &prior
	}
	return in, nil
}

func (rt *Router) decisionID(w http.ResponseWriter, r *http.Request) (id.DecisionID, bool) {
	decisionID, err := id.ParseDecisionID(r.PathValue("decisionId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("decisionId", errs.ReasonInvalidValue,
			"That is not a valid decision identifier."))
		return id.DecisionID{}, false
	}
	return decisionID, true
}

func (rt *Router) handleGetDecision(w http.ResponseWriter, r *http.Request) {
	if rt.determinationUnavailable(w, r) {
		return
	}
	decisionID, ok := rt.decisionID(w, r)
	if !ok {
		return
	}
	d, err := rt.Determination.Decision(r.Context(), decisionID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toDecision(d.Record, d.Result))
}

func (rt *Router) handleReplay(w http.ResponseWriter, r *http.Request) {
	if rt.determinationUnavailable(w, r) {
		return
	}
	decisionID, ok := rt.decisionID(w, r)
	if !ok {
		return
	}
	report, err := rt.Determination.Replay(r.Context(), decisionID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.ReplayReport{
		DecisionID:     report.DecisionID.String(),
		Verdict:        gen.ReplayVerdict(report.Verdict),
		EnvelopeDigest: report.EnvelopeDigest.String(),
		RecordedResult: report.RecordedResult.String(),
	}
	if !report.ReplayedResult.IsZero() {
		s := report.ReplayedResult.String()
		out.ReplayedResult = &s
	}
	if report.Divergence != "" {
		// The contract promises a path and never a value. An evaluation that
		// failed on replay is reported by the evaluator's own message, which
		// can quote what it read — so the client gets the fact, and the log
		// gets the message.
		divergence := report.Divergence
		if !strings.HasPrefix(divergence, "$") {
			rt.log.WarnContext(r.Context(), "replay diverged", "decision_id", report.DecisionID.String(), "divergence", divergence)
			divergence = "(evaluation failed on replay)"
		}
		out.Divergence = &divergence
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}
