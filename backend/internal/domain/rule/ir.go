// Package rule is the compiled-content execution model (ADR-0005).
//
// Tax logic is not Go. ADR-0001 §2.5 makes the rule DSL a versioned content
// artifact that compiles to a signed bundle on the CONTENT train and is
// executed by — never embedded in — this runtime. So what lives here is an
// interpreter over an intermediate representation, and the legal content lives
// somewhere this package cannot reach.
//
// The executor is structurally incapable of non-determinism (ADR-0005 §2.4).
// That is a claim about imports as much as about code: this package imports no
// clock, no random source, no network, no filesystem and no database, and the
// evaluation frame has nowhere to put one. A future change that wanted to read
// the time here would have to add an import that the depguard rules reject.
//
// Note what is deliberately absent: there is no compiler. Authoring, compiling
// and signing happen on the Z4 content plane (ADR-0005 §2.1), never at request
// time and never inside a cell. This package loads and runs; it does not build.
package rule

import (
	"fmt"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
)

// IRVersion is the instruction-set version this runtime implements.
//
// A bundle declares the minimum it requires and this runtime declares what it
// supports; the pair is checked at load and recorded in the release evidence
// manifest (ADR-0005 §2.8). A runtime that cannot execute a bundle refuses to
// load it and stays on the previous one rather than degrading.
const IRVersion = 1

// MinSupportedIRVersion is the oldest bundle this runtime will execute. Old
// bundles must keep running: a replay of a decision from two years ago runs the
// bundle that produced it.
const MinSupportedIRVersion = 1

// Op is the closed instruction set.
//
// Closed is the operative word. Adding an op is a SCHEMA train change with an
// IRVersion bump and a new golden-vector set, because a bundle compiled against
// a larger instruction set cannot be executed by a runtime that predates it and
// must be refused rather than partially understood.
type Op string

// The instructions. Every arithmetic op routes through internal/domain/fiscal
// carrying the rounding policy from its own node (ADR-0005 §2.3), so there is
// no arithmetic in this package at all.
const (
	// OpConst pushes a constant from the bundle's pool.
	OpConst Op = "CONST"
	// OpInput reads a named field from the canonical input.
	OpInput Op = "INPUT"
	// OpAccumulator reads a value the caller read before evaluation began
	// (ADR-0005 §2.5). The executor cannot query.
	OpAccumulator Op = "ACCUMULATOR"

	// OpAdd, OpSub and OpMul are exact. An inexact result is a defect and
	// surfaces as an error (ADR-0002 §2.1).
	OpAdd Op = "ADD"
	OpSub Op = "SUB"
	OpMul Op = "MUL"

	// OpApplyRate is base × rate under the node's rounding policy. It is the
	// shape almost every tax computation takes.
	OpApplyRate Op = "APPLY_RATE"
	// OpQuo divides under the node's rounding policy.
	OpQuo Op = "QUO"
	// OpRound materialises a value at the policy's scale.
	OpRound Op = "ROUND"

	// OpCompare yields a Bool.
	OpCompare Op = "COMPARE"
	// OpSelect is a total conditional: both branches are evaluated nodes, so
	// there is no control flow and the DAG stays a DAG.
	OpSelect Op = "SELECT"
	// OpAnd, OpOr and OpNot combine Bools.
	OpAnd Op = "AND"
	OpOr  Op = "OR"
	OpNot Op = "NOT"

	// OpEmit names a result: a tax component, a taxable base, a reason code.
	OpEmit Op = "EMIT"
	// OpRefuse ends evaluation with a recorded outcome rather than an error.
	// ADR-0016 §2.1: a refusal is a decision with evidence, not an error that
	// records nothing.
	OpRefuse Op = "REFUSE"
)

func (o Op) valid() bool {
	switch o {
	case OpConst, OpInput, OpAccumulator, OpAdd, OpSub, OpMul, OpApplyRate,
		OpQuo, OpRound, OpCompare, OpSelect, OpAnd, OpOr, OpNot, OpEmit, OpRefuse:
		return true
	}
	return false
}

// Type is the closed value domain of ADR-0005 §2.2.
type Type string

// The types a node may carry.
const (
	TypeMoney    Type = "MONEY"
	TypeRate     Type = "RATE"
	TypeQuantity Type = "QUANTITY"
	TypeBool     Type = "BOOL"
	TypeString   Type = "STRING"
	TypeReason   Type = "REASON_CODE"
)

// Comparison is the closed set of comparison operators.
type Comparison string

// The comparisons.
const (
	CmpEq  Comparison = "EQ"
	CmpNe  Comparison = "NE"
	CmpLt  Comparison = "LT"
	CmpLte Comparison = "LTE"
	CmpGt  Comparison = "GT"
	CmpGte Comparison = "GTE"
)

// NodeID identifies a node within one bundle.
type NodeID string

// Node is one operation in the rule DAG.
//
// Edges are data dependencies, named by Args. There is no control flow: a
// conditional is OpSelect over two already-evaluated nodes, which is what keeps
// the graph acyclic and the cost of an evaluation bounded by the node count
// rather than by the data.
type Node struct {
	ID   NodeID `json:"id"`
	Op   Op     `json:"op"`
	Type Type   `json:"type"`
	// Args names the nodes this one consumes, in order. Order is significant
	// for SUB and QUO, so it is a slice and never a set.
	Args []NodeID `json:"args,omitempty"`

	// Const names an entry in the bundle's constant pool, for OpConst.
	Const string `json:"const,omitempty"`
	// Field names an input path, for OpInput, or an accumulator key, for
	// OpAccumulator.
	Field string `json:"field,omitempty"`
	// Comparison is the operator, for OpCompare.
	Comparison Comparison `json:"comparison,omitempty"`
	// Policy is the rounding instruction, required for OpApplyRate, OpQuo and
	// OpRound. ADR-0002 §2.2: there is no default, and content supplies it.
	Policy *fiscal.RoundingPolicy `json:"policy,omitempty"`
	// Emit names the result slot, for OpEmit.
	Emit string `json:"emit,omitempty"`
	// Reason is the reason code, for OpRefuse and for a REASON_CODE constant.
	Reason errs.ReasonCode `json:"reason,omitempty"`

	// RuleVersion is the content version that authored this node. It travels
	// into the execution trace so a decision can name which rule produced each
	// step (ADR-0005 §2.7).
	RuleVersion string `json:"ruleVersion"`
	// RuleSemanticID identifies the rule's meaning across re-authorings
	// (ADR-0012 §2.4).
	RuleSemanticID string `json:"ruleSemanticId"`
}

// validate checks a node's shape against its op.
//
// This runs at bundle load, not at evaluation. ADR-0005 §2.6 rejects a bundle
// whole rather than partially activating one, so every node is checked before
// any request can reach it — an evaluation never encounters a malformed node.
func (n Node) validate() error {
	if n.ID == "" {
		return fmt.Errorf("rule: node has no id")
	}
	if !n.Op.valid() {
		return fmt.Errorf("rule: node %s has unknown op %q", n.ID, n.Op)
	}

	needsPolicy := n.Op == OpApplyRate || n.Op == OpQuo || n.Op == OpRound
	if needsPolicy && n.Policy == nil {
		// ADR-0002 §5.1 control 5 makes this a content-schema rejection. This
		// is the runtime's own check for content that reached it anyway, and it
		// refuses the bundle rather than picking a rounding mode.
		return fmt.Errorf("rule: node %s (%s) names no rounding policy", n.ID, n.Op)
	}
	if !needsPolicy && n.Policy != nil {
		return fmt.Errorf("rule: node %s (%s) carries a rounding policy but does not round", n.ID, n.Op)
	}

	arity := map[Op]int{
		OpAdd: 2, OpSub: 2, OpMul: 2, OpApplyRate: 2, OpQuo: 2,
		OpRound: 1, OpCompare: 2, OpSelect: 3, OpAnd: 2, OpOr: 2, OpNot: 1,
		OpEmit: 1, OpConst: 0, OpInput: 0, OpAccumulator: 0, OpRefuse: 0,
	}
	if want, ok := arity[n.Op]; ok && len(n.Args) != want {
		return fmt.Errorf("rule: node %s (%s) takes %d arguments, has %d", n.ID, n.Op, want, len(n.Args))
	}

	switch n.Op {
	case OpCompare:
		switch n.Comparison {
		case CmpEq, CmpNe, CmpLt, CmpLte, CmpGt, CmpGte:
		default:
			return fmt.Errorf("rule: node %s has unknown comparison %q", n.ID, n.Comparison)
		}
	case OpInput, OpAccumulator:
		if n.Field == "" {
			return fmt.Errorf("rule: node %s (%s) names no field", n.ID, n.Op)
		}
	case OpEmit:
		if n.Emit == "" {
			return fmt.Errorf("rule: node %s names no result slot", n.ID)
		}
	case OpRefuse:
		if n.Reason == "" {
			return fmt.Errorf("rule: node %s names no reason code", n.ID)
		}
		if !errs.Registered(n.Reason) {
			// An unregistered reason code in content is content that can
			// produce a decision nobody can interpret (ADR-0016 §2.4).
			return fmt.Errorf("rule: node %s names unregistered reason code %q", n.ID, n.Reason)
		}
	}

	if n.RuleVersion == "" || n.RuleSemanticID == "" {
		// Every node has to name the rule that authored it, or the execution
		// trace cannot say which rule produced a step and the decision does not
		// explain (ADR-0005 §2.7).
		return fmt.Errorf("rule: node %s names no rule version or semantic id", n.ID)
	}
	return nil
}
