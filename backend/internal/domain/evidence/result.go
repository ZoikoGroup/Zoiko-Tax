package evidence

import (
	"fmt"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Result is what a determination concluded, as the canonical document that is
// stored, digested, sealed and replayed.
//
// It names its envelope by digest rather than embedding it. The envelope is
// already an evidence object in its own right; embedding it would store the
// input twice, and a reader comparing the two copies would be comparing a
// thing with itself.
type Result struct {
	EnvelopeDigest canonical.Digest
	Outcome        Outcome
	// Reason says why the outcome is what it is. ADVISORY carries
	// NOT_AUTHORITATIVE, so a reader of the evidence never has to infer that
	// from an absence.
	Reason errs.ReasonCode
	// Emitted maps result slot to value, for the slots the content filled.
	Emitted map[string]rule.Value
	// Trace is every node visited, in evaluation order (ADR-0005 §2.7).
	Trace []rule.TraceStep
}

// Validate refuses a result that could not be meaningfully stored.
func (r Result) Validate() error {
	switch {
	case r.EnvelopeDigest.IsZero():
		return fmt.Errorf("evidence: result names no envelope")
	case !r.Outcome.Valid():
		return fmt.Errorf("evidence: result outcome %q is not a recorded outcome", r.Outcome)
	case r.Reason == "":
		return fmt.Errorf("evidence: result outcome %s carries no reason", r.Outcome)
	case len(r.Trace) == 0:
		// Every evaluation visits at least one node. An empty trace is a
		// result nobody can explain, which UX-05 and the replay both need.
		return fmt.Errorf("evidence: result carries no execution trace")
	}
	return nil
}

// Canonical renders the result for digesting.
//
// "emitted" is written even when empty: a refusal that emitted nothing has
// said so, and that is different from a document that does not say.
func (r Result) Canonical() (canonical.Value, error) {
	emitted := make([]canonical.Field, 0, len(r.Emitted))
	for slot, v := range r.Emitted {
		cv, err := valueOf(v)
		if err != nil {
			return canonical.Value{}, fmt.Errorf("evidence: emitted slot %q: %w", slot, err)
		}
		emitted = append(emitted, canonical.F(slot, cv))
	}
	return canonical.Object(
		canonical.F("envelopeDigest", canonical.String(r.EnvelopeDigest.String())),
		canonical.F("outcome", canonical.String(string(r.Outcome))),
		canonical.F("reason", canonical.String(string(r.Reason))),
		canonical.F("emitted", canonical.Object(emitted...)),
		canonical.F("trace", CanonicalTrace(r.Trace)),
	), nil
}

// Encode renders the result in canonical form, after validating it.
func (r Result) Encode() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	v, err := r.Canonical()
	if err != nil {
		return nil, err
	}
	return canonical.Encode(v)
}

// valueOf renders one evaluated value with its type, so that "21.00" is never
// separated from whether it was money, a rate or a quantity.
func valueOf(v rule.Value) (canonical.Value, error) {
	typed := func(fields ...canonical.Field) canonical.Value {
		return canonical.Object(append([]canonical.Field{canonical.F("type", canonical.String(string(v.Type)))}, fields...)...)
	}
	switch v.Type {
	case rule.TypeMoney:
		return typed(
			canonical.F("amount", canonical.Money(v.Money)),
			canonical.F("currency", canonical.String(string(v.Money.Currency()))),
		), nil
	case rule.TypeRate:
		return typed(
			canonical.F("value", canonical.Rate(v.Rate)),
			canonical.F("basis", canonical.String(string(v.Rate.Basis()))),
		), nil
	case rule.TypeQuantity:
		return typed(
			canonical.F("value", canonical.Quantity(v.Quantity)),
			canonical.F("unit", canonical.String(string(v.Quantity.Unit()))),
		), nil
	case rule.TypeBool:
		return typed(canonical.F("value", canonical.Bool(v.Bool))), nil
	case rule.TypeString:
		return typed(canonical.F("value", canonical.String(v.String))), nil
	case rule.TypeReason:
		return typed(canonical.F("value", canonical.String(string(v.Reason)))), nil
	}
	return canonical.Value{}, fmt.Errorf("unsupported value type %q", v.Type)
}
