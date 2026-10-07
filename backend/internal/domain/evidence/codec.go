package evidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// EncodeEnvelope renders an envelope as the canonical bytes that are stored
// and digested. It validates first: an envelope that cannot support a replay
// is never written.
func EncodeEnvelope(e Envelope) ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return canonical.Encode(e.Canonical())
}

// DecodeEnvelope reads an envelope written by EncodeEnvelope.
//
// It is strict in the way ADR-0011 makes a reader strict. An unknown member is
// rejected (P5), a fiscal quantity that arrives as a JSON number is rejected
// (P1), and — the check that makes the others complete — the decoded envelope
// is re-encoded and must reproduce the input byte for byte. A document that
// decodes but does not round-trip is one that some other reader could
// interpret differently: a null that decoded as an empty string, an exponent
// that parsed as the same number, a member order nobody canonicalized. Any of
// those is refused rather than replayed, because a replay of a
// reinterpretation is not a replay.
//
// encoding/json is used to read and never to write. ADR-0011 §2.7's
// prohibition is on serializing evidence with it, and the round-trip check is
// what guarantees this reader agrees with canonical's writer.
func DecodeEnvelope(data []byte) (Envelope, error) {
	var w envelopeWire
	if err := strictDecode(data, &w); err != nil {
		return Envelope{}, fmt.Errorf("evidence: decode envelope: %w", err)
	}
	if w.CanonProfile != canonical.ProfileVersion {
		// canon/v1 is the only profile this build implements. A record under
		// another profile is replayed by a build that implements it, and never
		// by this one pretending to.
		return Envelope{}, fmt.Errorf("evidence: envelope is under profile %q, this build implements %q",
			w.CanonProfile, canonical.ProfileVersion)
	}

	decisionTime, err := parseInstant(w.DecisionTime)
	if err != nil {
		return Envelope{}, fmt.Errorf("evidence: envelope decisionTime: %w", err)
	}
	eventTime, err := parseInstant(w.EventTime)
	if err != nil {
		return Envelope{}, fmt.Errorf("evidence: envelope eventTime: %w", err)
	}
	input, err := w.Input.decode()
	if err != nil {
		return Envelope{}, err
	}
	accumulators, err := decodeMoneyMap(w.Accumulators)
	if err != nil {
		return Envelope{}, fmt.Errorf("evidence: envelope accumulators: %w", err)
	}

	e := Envelope{
		DecisionTime: decisionTime,
		EventTime:    eventTime,
		BundleID:     w.BundleID,
		BundleDigest: w.BundleDigest,
		IRVersion:    w.IRVersion,
		CanonProfile: w.CanonProfile,
		Trains: Trains{
			App: w.Trains.App, Content: w.Trains.Content, AI: w.Trains.AI, Adapter: w.Trains.Adapter,
			Infra: w.Trains.Infra, Schema: w.Trains.Schema, Migration: w.Trains.Migration,
		},
		Input:        input,
		Accumulators: accumulators,
	}

	again, err := EncodeEnvelope(e)
	if err != nil {
		return Envelope{}, err
	}
	if !bytes.Equal(again, data) {
		return Envelope{}, fmt.Errorf("evidence: envelope is not in %s form", canonical.ProfileVersion)
	}
	return e, nil
}

// DecodeResult reads a result written by Result.Encode, under the same rules as
// DecodeEnvelope: unknown members and JSON numbers are refused, and the decoded
// result must re-encode to the input byte for byte.
//
// It exists for reading a recorded decision back — what it emitted, and why —
// and never for replay. A replay re-evaluates and compares bytes; it does not
// need to understand the recorded result, and would be weaker if it did.
func DecodeResult(data []byte) (Result, error) {
	var w resultWire
	if err := strictDecode(data, &w); err != nil {
		return Result{}, fmt.Errorf("evidence: decode result: %w", err)
	}
	envDigest, err := canonical.ParseDigest(w.EnvelopeDigest)
	if err != nil {
		return Result{}, fmt.Errorf("evidence: result envelopeDigest: %w", err)
	}
	r := Result{
		EnvelopeDigest: envDigest,
		Outcome:        Outcome(w.Outcome),
		Reason:         errs.ReasonCode(w.Reason),
		Emitted:        make(map[string]rule.Value, len(w.Emitted)),
		Trace:          make([]rule.TraceStep, 0, len(w.Trace)),
	}
	for slot, v := range w.Emitted {
		decoded, err := v.decode()
		if err != nil {
			return Result{}, fmt.Errorf("evidence: result emitted slot %q: %w", slot, err)
		}
		r.Emitted[slot] = decoded
	}
	for _, s := range w.Trace {
		args := make([]rule.NodeID, 0, len(s.Args))
		for _, a := range s.Args {
			args = append(args, rule.NodeID(a))
		}
		if len(args) == 0 {
			args = nil
		}
		r.Trace = append(r.Trace, rule.TraceStep{
			Node: rule.NodeID(s.Node), Op: rule.Op(s.Op), Args: args,
			Output: s.Output, OutputType: rule.Type(s.OutputType),
			RuleVersion: s.RuleVersion, RuleSemanticID: s.RuleSemanticID, Policy: s.Policy,
		})
	}

	again, err := r.Encode()
	if err != nil {
		return Result{}, err
	}
	if !bytes.Equal(again, data) {
		return Result{}, fmt.Errorf("evidence: result is not in %s form", canonical.ProfileVersion)
	}
	return r, nil
}

// ---------------------------------------------------------------------------
// wire forms — read-only
// ---------------------------------------------------------------------------

type resultWire struct {
	EnvelopeDigest string               `json:"envelopeDigest"`
	Outcome        string               `json:"outcome"`
	Reason         string               `json:"reason"`
	Emitted        map[string]valueWire `json:"emitted"`
	Trace          []traceWire          `json:"trace"`
}

// valueWire is every member a typed value can carry. Which ones are present is
// decided by the type, and decode refuses a combination valueOf would not
// write — the round-trip check would catch it anyway, but not by name.
type valueWire struct {
	Type     string `json:"type"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Basis    string `json:"basis"`
	Unit     string `json:"unit"`
	// Value is a string for every type but BOOL. RawMessage keeps the two
	// apart until the type says which it is.
	Value json.RawMessage `json:"value"`
}

func (w valueWire) decode() (rule.Value, error) {
	v := rule.Value{Type: rule.Type(w.Type)}
	text := func() (string, error) {
		var s string
		if err := json.Unmarshal(w.Value, &s); err != nil {
			return "", fmt.Errorf("%s value is not a string", w.Type)
		}
		return s, nil
	}
	var err error
	switch v.Type {
	case rule.TypeMoney:
		v.Money, err = fiscal.ParseMoney(w.Amount, fiscal.Currency(w.Currency))
	case rule.TypeRate:
		var s string
		if s, err = text(); err == nil {
			v.Rate, err = fiscal.ParseRate(s, fiscal.RateBasis(w.Basis))
		}
	case rule.TypeQuantity:
		var s string
		if s, err = text(); err == nil {
			v.Quantity, err = fiscal.ParseQuantity(s, fiscal.Unit(w.Unit))
		}
	case rule.TypeBool:
		if json.Unmarshal(w.Value, &v.Bool) != nil {
			err = fmt.Errorf("BOOL value is not a boolean")
		}
	case rule.TypeString:
		v.String, err = text()
	case rule.TypeReason:
		var s string
		s, err = text()
		v.Reason = errs.ReasonCode(s)
	default:
		err = fmt.Errorf("unsupported value type %q", w.Type)
	}
	return v, err
}

type traceWire struct {
	Node           string   `json:"node"`
	Op             string   `json:"op"`
	Args           []string `json:"args"`
	Output         string   `json:"output"`
	OutputType     string   `json:"outputType"`
	RuleVersion    string   `json:"ruleVersion"`
	RuleSemanticID string   `json:"ruleSemanticId"`
	Policy         string   `json:"policy"`
}

type envelopeWire struct {
	DecisionTime string               `json:"decisionTime"`
	EventTime    string               `json:"eventTime"`
	BundleID     string               `json:"bundleId"`
	BundleDigest string               `json:"bundleDigest"`
	IRVersion    int                  `json:"irVersion"`
	CanonProfile string               `json:"canonProfile"`
	Trains       trainsWire           `json:"trains"`
	Input        inputWire            `json:"input"`
	Accumulators map[string]moneyWire `json:"accumulators"`
}

type trainsWire struct {
	App       string `json:"app"`
	Content   string `json:"content"`
	AI        string `json:"ai"`
	Adapter   string `json:"adapter"`
	Infra     string `json:"infra"`
	Schema    string `json:"schema"`
	Migration string `json:"migration"`
}

type inputWire struct {
	Money      map[string]moneyWire    `json:"money"`
	Rates      map[string]rateWire     `json:"rates"`
	Quantities map[string]quantityWire `json:"quantities"`
	Flags      map[string]bool         `json:"flags"`
	Strings    map[string]string       `json:"strings"`
}

type moneyWire struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type rateWire struct {
	Value string `json:"value"`
	Basis string `json:"basis"`
}

type quantityWire struct {
	Value string `json:"value"`
	Unit  string `json:"unit"`
}

func (w inputWire) decode() (Input, error) {
	money, err := decodeMoneyMap(w.Money)
	if err != nil {
		return Input{}, fmt.Errorf("evidence: input money: %w", err)
	}
	var in Input
	in.Money = money
	if len(w.Rates) > 0 {
		in.Rates = make(map[string]fiscal.Rate, len(w.Rates))
		for name, r := range w.Rates {
			v, err := fiscal.ParseRate(r.Value, fiscal.RateBasis(r.Basis))
			if err != nil {
				return Input{}, fmt.Errorf("evidence: input rate %q: %w", name, err)
			}
			in.Rates[name] = v
		}
	}
	if len(w.Quantities) > 0 {
		in.Quantities = make(map[string]fiscal.Quantity, len(w.Quantities))
		for name, q := range w.Quantities {
			v, err := fiscal.ParseQuantity(q.Value, fiscal.Unit(q.Unit))
			if err != nil {
				return Input{}, fmt.Errorf("evidence: input quantity %q: %w", name, err)
			}
			in.Quantities[name] = v
		}
	}
	if len(w.Flags) > 0 {
		in.Flags = w.Flags
	}
	if len(w.Strings) > 0 {
		in.Strings = w.Strings
	}
	return in, nil
}

func decodeMoneyMap(w map[string]moneyWire) (map[string]fiscal.Money, error) {
	if len(w) == 0 {
		return nil, nil
	}
	out := make(map[string]fiscal.Money, len(w))
	for name, m := range w {
		v, err := fiscal.ParseMoney(m.Amount, fiscal.Currency(m.Currency))
		if err != nil {
			return nil, fmt.Errorf("%q: %w", name, err)
		}
		out[name] = v
	}
	return out, nil
}

// instantLayout is canon/v1's P2 form, the one canonical.FormatTime writes.
const instantLayout = "2006-01-02T15:04:05.000000Z"

func parseInstant(s string) (time.Time, error) {
	t, err := time.Parse(instantLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a canon/v1 instant", s)
	}
	return t.UTC(), nil
}

// strictDecode reads exactly one JSON document into v, refusing unknown
// members and trailing content.
func strictDecode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("more than one document")
	}
	return nil
}

// DecodeInput reads an input back from its canonical form (Input.Canonical),
// refusing unknown members. It is the parser an envelope's input goes
// through, so anything stored in canonical form and decoded here — a queued
// batch item — reaches evaluation exactly as a direct request would.
func DecodeInput(data []byte) (Input, error) {
	var w inputWire
	if err := strictDecode(data, &w); err != nil {
		return Input{}, fmt.Errorf("evidence: input: %w", err)
	}
	return w.decode()
}

// DecodeReadSet reads an accumulator read set back from ReadSetCanonical.
func DecodeReadSet(data []byte) (map[string]fiscal.Money, error) {
	var w map[string]moneyWire
	if err := strictDecode(data, &w); err != nil {
		return nil, fmt.Errorf("evidence: read set: %w", err)
	}
	m, err := decodeMoneyMap(w)
	if err != nil {
		return nil, fmt.Errorf("evidence: read set %w", err)
	}
	return m, nil
}
