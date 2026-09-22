package rule

import (
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
)

// Frame is everything an evaluation may see (ADR-0005 §2.5).
//
// This struct is the determinism guarantee. There is no clock in it, no
// database handle, no network client, no random source and no AI gateway — so
// the evaluator cannot reach one, and a future change that wanted to would have
// to add a field here, in a review, rather than adding an import somewhere
// quiet.
//
// Accumulator values are read before evaluation starts and passed in. The
// executor cannot query, which keeps evaluation pure and keeps the database
// interaction inside the transaction boundary ADR-0004 controls.
type Frame struct {
	// DecisionTime and EventTime arrive from the envelope, never from a clock
	// (ADR-0003 §2.5). They are values, so a replay supplies the historical
	// instants and gets the historical answer.
	DecisionTime time.Time
	EventTime    time.Time

	// Money, Rates and Quantities are the canonical input, already parsed.
	Money      map[string]fiscal.Money
	Rates      map[string]fiscal.Rate
	Quantities map[string]fiscal.Quantity
	Flags      map[string]bool
	Strings    map[string]string

	// Accumulators were read inside the transaction before evaluation began.
	Accumulators map[string]fiscal.Money
}

// Value is one evaluated result. Exactly one field is set, matching Type.
type Value struct {
	Type     Type
	Money    fiscal.Money
	Rate     fiscal.Rate
	Quantity fiscal.Quantity
	Bool     bool
	String   string
	Reason   errs.ReasonCode
}

// TraceStep is one node's contribution to the execution trace.
//
// ADR-0005 §2.7 makes this evidence rather than telemetry: it is written to the
// evidence store, sealed, and retained under statutory retention, and it is the
// replay oracle and the UX-05 explanation. It is deliberately not an
// OpenTelemetry span, which is operational and retained for days.
type TraceStep struct {
	Node           NodeID   `json:"node"`
	Op             Op       `json:"op"`
	Args           []NodeID `json:"args,omitempty"`
	Output         string   `json:"output"`
	OutputType     Type     `json:"outputType"`
	RuleVersion    string   `json:"ruleVersion"`
	RuleSemanticID string   `json:"ruleSemanticId"`
	// Policy is the rounding that applied, by name, for the steps that round.
	// ADR-0002 §5.1 control 3: a decision that cannot name its rounding does
	// not replay.
	Policy string `json:"policy,omitempty"`
}

// Result is what an evaluation produced.
type Result struct {
	// Emitted maps result slot to value, for the nodes that reached OpEmit.
	Emitted map[string]Value
	// Trace is every node visited, in evaluation order.
	Trace []TraceStep
	// Refused is set when the content declined to produce a figure. This is a
	// decision with evidence, not an error (ADR-0016 §2.1).
	Refused bool
	Reason  errs.ReasonCode
}

// Evaluate walks the DAG in topological order.
//
// It is a loop over a precomputed order, not a recursion: evaluation cost is
// bounded by the node count, there is no stack to overflow on deep content, and
// the order is fixed at load so two replicas produce identical traces.
func Evaluate(b *Bundle, f Frame) (Result, error) {
	if b == nil {
		return Result{}, errs.New(errs.CategoryUnavailable, errs.ReasonNoContentBundle,
			"The cell has no active content bundle. The request was not applied and may be retried.")
	}

	values := make(map[NodeID]Value, len(b.order))
	result := Result{
		Emitted: map[string]Value{},
		Trace:   make([]TraceStep, 0, len(b.order)),
	}

	for _, id := range b.order {
		n := b.nodes[id]

		if n.Op == OpRefuse {
			// A refusal ends the evaluation with a recorded outcome. The trace
			// up to this point is kept: "why did it refuse" is answered by the
			// steps that led here, and discarding them would make the refusal
			// unexplainable.
			result.Trace = append(result.Trace, TraceStep{
				Node: n.ID, Op: n.Op, Output: string(n.Reason), OutputType: TypeReason,
				RuleVersion: n.RuleVersion, RuleSemanticID: n.RuleSemanticID,
			})
			result.Refused = true
			result.Reason = n.Reason
			return result, nil
		}

		v, err := evalNode(b, f, n, values)
		if err != nil {
			return Result{}, err
		}
		values[id] = v

		step := TraceStep{
			Node: n.ID, Op: n.Op, Args: n.Args,
			Output: render(v), OutputType: v.Type,
			RuleVersion: n.RuleVersion, RuleSemanticID: n.RuleSemanticID,
		}
		if n.Policy != nil {
			step.Policy = n.Policy.String()
		}
		result.Trace = append(result.Trace, step)

		if n.Op == OpEmit {
			result.Emitted[n.Emit] = v
		}
	}
	return result, nil
}

func evalNode(b *Bundle, f Frame, n Node, values map[NodeID]Value) (Value, error) {
	arg := func(i int) Value { return values[n.Args[i]] }

	switch n.Op {
	case OpConst:
		return b.constant(n.Const, n.Type)

	case OpInput:
		return inputValue(f, n)

	case OpAccumulator:
		m, ok := f.Accumulators[n.Field]
		if !ok {
			// A missing accumulator is not zero. Treating it as zero would make
			// a threshold rule silently pass on the first transaction after a
			// read failed, so it is refused.
			return Value{}, fmt.Errorf("rule: node %s reads accumulator %q, which was not supplied", n.ID, n.Field)
		}
		return Value{Type: TypeMoney, Money: m}, nil

	case OpAdd, OpSub:
		a, c := arg(0), arg(1)
		if a.Type != TypeMoney || c.Type != TypeMoney {
			return Value{}, typeErr(n, "MONEY and MONEY")
		}
		var (
			out fiscal.Money
			err error
		)
		if n.Op == OpAdd {
			out, err = a.Money.Add(c.Money)
		} else {
			out, err = a.Money.Sub(c.Money)
		}
		if err != nil {
			return Value{}, err
		}
		return Value{Type: TypeMoney, Money: out}, nil

	case OpMul:
		// Money × Quantity would be a per-unit extension, which is OpApplyRate
		// with a PER_UNIT rate. MUL is deliberately only Money × Rate, so that
		// every multiplication that lands at a currency scale carries a policy.
		return Value{}, fmt.Errorf("rule: node %s uses MUL; use APPLY_RATE so the rounding policy is explicit", n.ID)

	case OpApplyRate:
		base, rate := arg(0), arg(1)
		if rate.Type != TypeRate {
			return Value{}, typeErr(n, "a RATE as the second argument")
		}
		switch base.Type {
		case TypeMoney:
			out, err := base.Money.ApplyRate(rate.Rate, *n.Policy)
			if err != nil {
				return Value{}, err
			}
			return Value{Type: TypeMoney, Money: out}, nil
		case TypeQuantity:
			// A per-unit charge: quantity × rate becomes money. The currency
			// comes from content, because a quantity does not carry one.
			currency := fiscal.Currency(b.stringConsts[n.Field])
			if currency == "" {
				return Value{}, fmt.Errorf("rule: node %s extends a quantity but names no currency constant", n.ID)
			}
			out, err := base.Quantity.ExtendPerUnit(rate.Rate, currency, *n.Policy)
			if err != nil {
				return Value{}, err
			}
			return Value{Type: TypeMoney, Money: out}, nil
		}
		return Value{}, typeErr(n, "MONEY or QUANTITY as the first argument")

	case OpRound:
		v := arg(0)
		if v.Type != TypeMoney {
			return Value{}, typeErr(n, "MONEY")
		}
		out, err := v.Money.Round(*n.Policy)
		if err != nil {
			return Value{}, err
		}
		return Value{Type: TypeMoney, Money: out}, nil

	case OpQuo:
		return Value{}, fmt.Errorf("rule: node %s uses QUO, which IR version %d does not implement for Money", n.ID, b.irVersion)

	case OpCompare:
		return compare(n, arg(0), arg(1))

	case OpSelect:
		cond := arg(0)
		if cond.Type != TypeBool {
			return Value{}, typeErr(n, "a BOOL condition")
		}
		// Both branches were already evaluated, because both are nodes in the
		// DAG. There is no short-circuit and no control flow, which is what
		// keeps evaluation cost independent of the data.
		if cond.Bool {
			return arg(1), nil
		}
		return arg(2), nil

	case OpAnd, OpOr:
		a, c := arg(0), arg(1)
		if a.Type != TypeBool || c.Type != TypeBool {
			return Value{}, typeErr(n, "BOOL and BOOL")
		}
		if n.Op == OpAnd {
			return Value{Type: TypeBool, Bool: a.Bool && c.Bool}, nil
		}
		return Value{Type: TypeBool, Bool: a.Bool || c.Bool}, nil

	case OpNot:
		a := arg(0)
		if a.Type != TypeBool {
			return Value{}, typeErr(n, "BOOL")
		}
		return Value{Type: TypeBool, Bool: !a.Bool}, nil

	case OpEmit:
		return arg(0), nil
	}
	return Value{}, fmt.Errorf("rule: node %s has unhandled op %q", n.ID, n.Op)
}

func inputValue(f Frame, n Node) (Value, error) {
	switch n.Type {
	case TypeMoney:
		if v, ok := f.Money[n.Field]; ok {
			return Value{Type: TypeMoney, Money: v}, nil
		}
	case TypeRate:
		if v, ok := f.Rates[n.Field]; ok {
			return Value{Type: TypeRate, Rate: v}, nil
		}
	case TypeQuantity:
		if v, ok := f.Quantities[n.Field]; ok {
			return Value{Type: TypeQuantity, Quantity: v}, nil
		}
	case TypeBool:
		if v, ok := f.Flags[n.Field]; ok {
			return Value{Type: TypeBool, Bool: v}, nil
		}
	case TypeString:
		if v, ok := f.Strings[n.Field]; ok {
			return Value{Type: TypeString, String: v}, nil
		}
	}
	// An absent input is refused rather than defaulted. A rule that reads a
	// field the transaction did not carry is a rule applied to a transaction it
	// was not written for, and a zero would hide that.
	return Value{}, fmt.Errorf("rule: node %s reads %s input %q, which the canonical input does not carry", n.ID, n.Type, n.Field)
}

func (b *Bundle) constant(name string, t Type) (Value, error) {
	switch t {
	case TypeMoney:
		return Value{Type: TypeMoney, Money: b.moneyConsts[name]}, nil
	case TypeRate:
		return Value{Type: TypeRate, Rate: b.rateConsts[name]}, nil
	case TypeQuantity:
		return Value{Type: TypeQuantity, Quantity: b.quantityConsts[name]}, nil
	case TypeString:
		return Value{Type: TypeString, String: b.stringConsts[name]}, nil
	case TypeReason:
		return Value{Type: TypeReason, Reason: errs.ReasonCode(b.stringConsts[name])}, nil
	}
	return Value{}, fmt.Errorf("rule: constant %q has unsupported type %q", name, t)
}

func compare(n Node, a, b Value) (Value, error) {
	if a.Type != b.Type {
		return Value{}, fmt.Errorf("rule: node %s compares %s with %s", n.ID, a.Type, b.Type)
	}

	switch a.Type {
	case TypeMoney:
		// Comparison goes through Sub so that a currency mismatch is an error
		// rather than a silent false. Two amounts in different currencies are
		// not unequal; the question does not have an answer.
		diff, err := a.Money.Sub(b.Money)
		if err != nil {
			return Value{}, err
		}
		return Value{Type: TypeBool, Bool: compareSign(n.Comparison, signOf(diff))}, nil

	case TypeBool:
		switch n.Comparison {
		case CmpEq:
			return Value{Type: TypeBool, Bool: a.Bool == b.Bool}, nil
		case CmpNe:
			return Value{Type: TypeBool, Bool: a.Bool != b.Bool}, nil
		}
		return Value{}, fmt.Errorf("rule: node %s orders BOOL values, which has no meaning", n.ID)

	case TypeString, TypeReason:
		equal := a.String == b.String && a.Reason == b.Reason
		switch n.Comparison {
		case CmpEq:
			return Value{Type: TypeBool, Bool: equal}, nil
		case CmpNe:
			return Value{Type: TypeBool, Bool: !equal}, nil
		}
		// Ordering strings would make the answer depend on a collation nobody
		// declared, which is a determinism hazard dressed as a convenience.
		return Value{}, fmt.Errorf("rule: node %s orders %s values; only EQ and NE are defined", n.ID, a.Type)
	}
	return Value{}, fmt.Errorf("rule: node %s compares unsupported type %s", n.ID, a.Type)
}

// signOf reports -1, 0 or 1 without reaching for a decimal comparison, which
// would need apd here.
func signOf(m fiscal.Money) int {
	if m.IsZero() {
		return 0
	}
	if len(m.CanonicalString()) > 0 && m.CanonicalString()[0] == '-' {
		return -1
	}
	return 1
}

func compareSign(c Comparison, sign int) bool {
	switch c {
	case CmpEq:
		return sign == 0
	case CmpNe:
		return sign != 0
	case CmpLt:
		return sign < 0
	case CmpLte:
		return sign <= 0
	case CmpGt:
		return sign > 0
	case CmpGte:
		return sign >= 0
	}
	return false
}

func typeErr(n Node, want string) error {
	return fmt.Errorf("rule: node %s (%s) expects %s", n.ID, n.Op, want)
}

// render produces the trace's textual form of a value. Fiscal quantities render
// in the ADR-0011 §2.2 normal form, so a trace digests stably.
func render(v Value) string {
	switch v.Type {
	case TypeMoney:
		return v.Money.CanonicalString() + " " + string(v.Money.Currency())
	case TypeRate:
		return v.Rate.CanonicalString()
	case TypeQuantity:
		return v.Quantity.CanonicalString() + " " + string(v.Quantity.Unit())
	case TypeBool:
		if v.Bool {
			return "true"
		}
		return "false"
	case TypeReason:
		return string(v.Reason)
	}
	return v.String
}
