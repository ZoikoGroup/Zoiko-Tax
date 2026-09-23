// Package compile turns a parsed rule source into a bundle manifest.
//
// This is the type checker and the DAG builder. It is the stage where a
// mistake costs the most and shows the least, which is why ADR-0005 §5.1
// control 3 puts mutation testing here: a compiler defect is a silent fiscal
// defect with the blast radius of every transaction the pack touches.
//
// The rule it works to is that **nothing this package emits may fail at
// evaluation for a reason a compilation could have found**. The runtime
// (internal/domain/rule) checks the same things again at load, and that
// duplication is deliberate — a cell must refuse a bundle no compiler of ours
// produced — but a bundle that loads and then errors on a real transaction is a
// compiler that did not do its job. So the checks here mirror the evaluator's
// switch statements one for one, including the ones the evaluator expresses as
// runtime errors: comparing two RATEs, selecting between branches of different
// types, applying a rate that is not a RATE.
//
// The compiler is not in the cell. ADR-0005 §2.1 puts compilation on the Z4
// content plane, and the depguard rule content-compiler-is-not-in-the-cell
// makes that structural: cmd/ztax-core cannot import this package.
package compile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zoikogroup/zoikotax/backend/internal/content/dsl"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
)

// Compile checks a program and builds its manifest.
//
// The returned manifest carries no digest: the digest is taken over the
// canonical bytes and recorded in the seal, because a document cannot contain
// its own digest (internal/content/bundle).
func Compile(prog *dsl.Program) (rule.Manifest, error) {
	c := &compiler{
		prog:      prog,
		policies:  map[string]fiscal.RoundingPolicy{},
		constants: map[string]rule.Constant{},
		slots:     map[string]dsl.Position{},
		rules:     map[string]dsl.Position{},
		ids:       map[rule.NodeID]dsl.Position{},
	}
	return c.run()
}

type compiler struct {
	prog *dsl.Program

	policies  map[string]fiscal.RoundingPolicy
	constants map[string]rule.Constant
	slots     map[string]dsl.Position
	rules     map[string]dsl.Position
	ids       map[rule.NodeID]dsl.Position

	nodes []rule.Node
	roots []rule.NodeID
}

// binding is one name in a rule's scope, and the node it resolves to.
type binding struct {
	node rule.NodeID
	typ  rule.Type
	pos  dsl.Position
	// constName is set for a binding that names a constant, because APPLY_RATE
	// over a quantity needs the constant's pool name rather than its node.
	constName string
	used      bool
}

func (c *compiler) run() (rule.Manifest, error) {
	if c.prog.BundleID == "" {
		return rule.Manifest{}, errorAt(c.prog.BundlePos, "the bundle has no identity")
	}
	if c.prog.IRVersion != rule.IRVersion {
		// Compiling for an instruction set this build does not implement would
		// produce a bundle whose refusal at load (ADR-0005 §2.8) is the first
		// anyone hears of it.
		return rule.Manifest{}, errorAt(c.prog.IRPos,
			"this compiler emits IR version %d; the source asks for %d", rule.IRVersion, c.prog.IRVersion)
	}

	for _, p := range c.prog.Policies {
		if err := c.declarePolicy(p); err != nil {
			return rule.Manifest{}, err
		}
	}
	for _, r := range c.prog.Rules {
		if err := c.compileRule(r); err != nil {
			return rule.Manifest{}, err
		}
	}
	if len(c.roots) == 0 && !c.refuses() {
		// A bundle that emits nothing and refuses nothing computes nothing. It
		// would load, evaluate every node and produce an empty result, which is
		// indistinguishable from a rule that did not apply.
		return rule.Manifest{}, errorAt(c.prog.BundlePos,
			"bundle %q emits no results and refuses nothing", c.prog.BundleID)
	}

	constants := make([]rule.Constant, 0, len(c.constants))
	for _, k := range c.constants {
		constants = append(constants, k)
	}
	sort.Slice(constants, func(i, j int) bool { return constants[i].Name < constants[j].Name })

	return rule.Manifest{
		BundleID:  c.prog.BundleID,
		IRVersion: c.prog.IRVersion,
		Nodes:     c.nodes,
		Roots:     c.roots,
		Constants: constants,
	}, nil
}

func (c *compiler) refuses() bool {
	for _, n := range c.nodes {
		if n.Op == rule.OpRefuse {
			return true
		}
	}
	return false
}

func (c *compiler) declarePolicy(p dsl.Policy) error {
	if _, dup := c.policies[p.Name]; dup {
		return errorAt(p.Pos, "policy %q is declared twice", p.Name)
	}
	// The policy is built by decoding, not by a literal: ADR-0002 §2.3 puts the
	// origin of every RoundingPolicy in signed content, and fiscal exports no
	// constructor. Rendering the source's words into the content wire form and
	// decoding them is the compiler doing exactly what a cell will do.
	wire := fmt.Sprintf(`{"mode":%q,"scale":%d,"basis":%q}`, p.Mode, p.Scale, p.Basis)
	policy, err := fiscal.DecodeRoundingPolicy([]byte(wire))
	if err != nil {
		return errorAt(p.Pos, "policy %q: %s", p.Name, strings.TrimPrefix(err.Error(), "fiscal: "))
	}
	c.policies[p.Name] = policy
	return nil
}

func (c *compiler) compileRule(r dsl.Rule) error {
	if !isSlug(r.Name) {
		// Rule names become node-identifier prefixes and reach an auditor
		// through the execution trace, so they are constrained to something
		// that reads the same everywhere it is printed.
		return errorAt(r.Pos, "rule name %q must be lowercase letters, digits and hyphens", r.Name)
	}
	if prior, dup := c.rules[r.Name]; dup {
		return errorAt(r.Pos, "rule %q is declared twice; the first is at %s", r.Name, prior)
	}
	c.rules[r.Name] = r.Pos
	if r.Version == "" {
		return errorAt(r.VersionPos, "rule %q names no content version", r.Name)
	}
	if r.SemanticID == "" {
		return errorAt(r.SemanticPos, "rule %q names no semantic id (ADR-0012 §2.4)", r.Name)
	}

	scope := map[string]*binding{}
	emitted := 0
	refused := false

	for _, stmt := range r.Statements {
		switch s := stmt.(type) {
		case dsl.ConstDecl:
			if err := c.constStmt(r, scope, s); err != nil {
				return err
			}
		case dsl.InputDecl:
			if err := c.inputStmt(r, scope, s); err != nil {
				return err
			}
		case dsl.AccumulatorDecl:
			if err := c.accumulatorStmt(r, scope, s); err != nil {
				return err
			}
		case dsl.LetStmt:
			if err := c.letStmt(r, scope, s); err != nil {
				return err
			}
		case dsl.EmitStmt:
			if err := c.emitStmt(r, scope, s); err != nil {
				return err
			}
			emitted++
		case dsl.RefuseStmt:
			if err := c.refuseStmt(r, s); err != nil {
				return err
			}
			refused = true
		default:
			return errorAt(stmt.Position(), "unhandled statement %T", stmt)
		}
	}

	if refused && (emitted > 0 || len(r.Statements) > 1) {
		// A refusal has no arguments in IR version 1, so it is reached
		// unconditionally and evaluation stops there. Anything else in the same
		// rule is either dead or misleading, and "misleading" in content that
		// decides a liability is the worse of the two.
		return errorAt(r.Pos,
			"rule %q mixes refuse with other statements; a refusal in IR version 1 is unconditional, so the rest could never run", r.Name)
	}
	if !refused && emitted == 0 {
		return errorAt(r.Pos, "rule %q emits nothing", r.Name)
	}

	for _, name := range sortedNames(scope) {
		if !scope[name].used {
			// An unreferenced binding is still evaluated — the executor walks
			// every node — so it costs latency and appears in the evidence
			// trace as a step that contributed to nothing. It is also what a
			// misspelled reference looks like from here.
			return errorAt(scope[name].pos, "rule %q binds %q and never uses it", r.Name, name)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// statements
// ---------------------------------------------------------------------------

func (c *compiler) constStmt(r dsl.Rule, scope map[string]*binding, s dsl.ConstDecl) error {
	if err := declare(scope, s.Name, s.Pos); err != nil {
		return err
	}
	qualified := r.Name + "." + s.Name

	var (
		k   rule.Constant
		typ rule.Type
	)
	switch s.Type {
	case "money":
		if _, err := fiscal.ParseMoney(s.Value, fiscal.Currency(s.Qualifier)); err != nil {
			return errorAt(s.Pos, "constant %q: %s", s.Name, strings.TrimPrefix(err.Error(), "fiscal: "))
		}
		typ = rule.TypeMoney
		k = rule.Constant{Name: qualified, Type: typ, Value: s.Value, Currency: s.Qualifier}
	case "rate":
		if _, err := fiscal.ParseRate(s.Value, fiscal.RateBasis(s.Qualifier)); err != nil {
			return errorAt(s.Pos, "constant %q: %s", s.Name, strings.TrimPrefix(err.Error(), "fiscal: "))
		}
		typ = rule.TypeRate
		k = rule.Constant{Name: qualified, Type: typ, Value: s.Value, Basis: s.Qualifier}
	case "quantity":
		if _, err := fiscal.ParseQuantity(s.Value, fiscal.Unit(s.Qualifier)); err != nil {
			return errorAt(s.Pos, "constant %q: %s", s.Name, strings.TrimPrefix(err.Error(), "fiscal: "))
		}
		typ = rule.TypeQuantity
		k = rule.Constant{Name: qualified, Type: typ, Value: s.Value, Unit: s.Qualifier}
	case "string":
		typ = rule.TypeString
		k = rule.Constant{Name: qualified, Type: typ, Value: s.Value}
	case "reason":
		if !errs.Registered(errs.ReasonCode(s.Value)) {
			// A reason code nobody registered is a decision nobody can
			// interpret (ADR-0016 §2.4), and content is where one would enter.
			return errorAt(s.Pos, "constant %q names reason code %q, which is not in the register", s.Name, s.Value)
		}
		typ = rule.TypeReason
		k = rule.Constant{Name: qualified, Type: typ, Value: s.Value}
	default:
		return errorAt(s.Pos, "constant %q has unknown type %q", s.Name, s.Type)
	}

	c.constants[qualified] = k
	node, err := c.emitNode(r, s.Name, s.Pos, rule.Node{Op: rule.OpConst, Type: typ, Const: qualified})
	if err != nil {
		return err
	}
	scope[s.Name] = &binding{node: node, typ: typ, pos: s.Pos, constName: qualified}
	return nil
}

func (c *compiler) inputStmt(r dsl.Rule, scope map[string]*binding, s dsl.InputDecl) error {
	if err := declare(scope, s.Name, s.Pos); err != nil {
		return err
	}
	typ, ok := inputTypes[s.Type]
	if !ok {
		return errorAt(s.Pos, "input %q has type %q; an input is money, rate, quantity, bool or string", s.Name, s.Type)
	}
	node, err := c.emitNode(r, s.Name, s.Pos, rule.Node{Op: rule.OpInput, Type: typ, Field: s.Field})
	if err != nil {
		return err
	}
	scope[s.Name] = &binding{node: node, typ: typ, pos: s.Pos}
	return nil
}

// inputTypes is the subset of the value domain a canonical input can carry.
// REASON_CODE is absent on purpose: a reason code is something content decides,
// never something a caller asserts about its own transaction.
var inputTypes = map[string]rule.Type{
	"money":    rule.TypeMoney,
	"rate":     rule.TypeRate,
	"quantity": rule.TypeQuantity,
	"bool":     rule.TypeBool,
	"string":   rule.TypeString,
}

func (c *compiler) accumulatorStmt(r dsl.Rule, scope map[string]*binding, s dsl.AccumulatorDecl) error {
	if err := declare(scope, s.Name, s.Pos); err != nil {
		return err
	}
	node, err := c.emitNode(r, s.Name, s.Pos, rule.Node{Op: rule.OpAccumulator, Type: rule.TypeMoney, Field: s.Key})
	if err != nil {
		return err
	}
	scope[s.Name] = &binding{node: node, typ: rule.TypeMoney, pos: s.Pos}
	return nil
}

func (c *compiler) emitStmt(r dsl.Rule, scope map[string]*binding, s dsl.EmitStmt) error {
	if prior, dup := c.slots[s.Slot]; dup {
		// Two rules writing one slot is a precedence question the content did
		// not answer, and ZTAX-DET-001 §5.5 forbids the runtime from inventing
		// an answer.
		return errorAt(s.Pos, "result slot %q is emitted twice; the first is at %s", s.Slot, prior)
	}
	c.slots[s.Slot] = s.Pos

	b, err := c.resolve(r, scope, s.Value)
	if err != nil {
		return err
	}
	node, err := c.emitNode(r, "emit-"+slugify(s.Slot), s.Pos, rule.Node{
		Op: rule.OpEmit, Type: b.typ, Args: []rule.NodeID{b.node}, Emit: s.Slot,
	})
	if err != nil {
		return err
	}
	c.roots = append(c.roots, node)
	return nil
}

func (c *compiler) refuseStmt(r dsl.Rule, s dsl.RefuseStmt) error {
	if !errs.Registered(errs.ReasonCode(s.Reason)) {
		return errorAt(s.Pos, "%q is not a registered reason code (ADR-0016 §2.4)", s.Reason)
	}
	if _, err := c.emitNode(r, "refuse", s.Pos, rule.Node{
		Op: rule.OpRefuse, Type: rule.TypeReason, Reason: errs.ReasonCode(s.Reason),
	}); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// expressions
// ---------------------------------------------------------------------------

func (c *compiler) letStmt(r dsl.Rule, scope map[string]*binding, s dsl.LetStmt) error {
	if err := declare(scope, s.Name, s.Pos); err != nil {
		return err
	}
	e := s.Expr

	args := make([]rule.NodeID, 0, len(e.Args))
	types := make([]rule.Type, 0, len(e.Args))
	for _, ref := range e.Args {
		b, err := c.resolve(r, scope, ref)
		if err != nil {
			return err
		}
		args = append(args, b.node)
		types = append(types, b.typ)
	}

	node := rule.Node{Args: args}
	var out rule.Type

	switch e.Op {
	case "add", "sub":
		if types[0] != rule.TypeMoney || types[1] != rule.TypeMoney {
			return errorAt(e.Pos, "%s takes MONEY and MONEY, got %s and %s", e.Op, types[0], types[1])
		}
		node.Op = map[string]rule.Op{"add": rule.OpAdd, "sub": rule.OpSub}[e.Op]
		out = rule.TypeMoney

	case "apply_rate":
		if types[1] != rule.TypeRate {
			return errorAt(e.Args[1].Pos, "apply_rate takes a RATE as its second operand, got %s", types[1])
		}
		switch types[0] {
		case rule.TypeMoney:
			if e.Currency != "" {
				// The base already carries a currency. A second one could only
				// agree redundantly or disagree silently.
				return errorAt(e.CurrencyPos, "apply_rate over MONEY takes its currency from the base; remove the currency clause")
			}
		case rule.TypeQuantity:
			if e.Currency == "" {
				return errorAt(e.Pos, "apply_rate over a QUANTITY produces money and must name the currency: add `currency <string constant>`")
			}
			cur, err := c.resolve(r, scope, dsl.Ref{Name: e.Currency, Pos: e.CurrencyPos})
			if err != nil {
				return err
			}
			if cur.typ != rule.TypeString || cur.constName == "" {
				return errorAt(e.CurrencyPos, "%q must be a string constant naming a currency code", e.Currency)
			}
			if _, err := fiscal.ParseMoney("0", fiscal.Currency(c.constants[cur.constName].Value)); err != nil {
				return errorAt(e.CurrencyPos, "%q holds %q, which is not a currency code", e.Currency, c.constants[cur.constName].Value)
			}
			// The evaluator reads the currency out of the constant pool by the
			// node's Field, so Field carries the pool name here rather than an
			// input path. It is the one place the two meanings of Field meet.
			node.Field = cur.constName
		default:
			return errorAt(e.Args[0].Pos, "apply_rate takes MONEY or QUANTITY as its first operand, got %s", types[0])
		}
		node.Op = rule.OpApplyRate
		out = rule.TypeMoney

	case "round":
		if types[0] != rule.TypeMoney {
			return errorAt(e.Args[0].Pos, "round takes MONEY, got %s", types[0])
		}
		node.Op = rule.OpRound
		out = rule.TypeMoney

	case "compare":
		cmp, ok := comparisons[e.Comparison]
		if !ok {
			return errorAt(e.Pos, "%q is not a comparison; expected eq, ne, lt, lte, gt or gte", e.Comparison)
		}
		if types[0] != types[1] {
			// Two amounts in different types are not unequal; the question does
			// not have an answer. Same reasoning the evaluator applies to two
			// currencies.
			return errorAt(e.Pos, "compare takes two operands of one type, got %s and %s", types[0], types[1])
		}
		switch types[0] {
		case rule.TypeMoney:
			// All six comparisons are defined.
		case rule.TypeBool, rule.TypeString, rule.TypeReason:
			if cmp != rule.CmpEq && cmp != rule.CmpNe {
				return errorAt(e.Pos, "ordering %s values would depend on a collation nobody declared; only eq and ne are defined", types[0])
			}
		default:
			return errorAt(e.Pos, "IR version 1 does not compare %s values", types[0])
		}
		node.Op = rule.OpCompare
		node.Comparison = cmp
		out = rule.TypeBool

	case "select":
		if types[0] != rule.TypeBool {
			return errorAt(e.Args[0].Pos, "select takes a BOOL condition, got %s", types[0])
		}
		if types[1] != types[2] {
			// The evaluator returns whichever branch the condition picks
			// without checking, so a mismatch here would surface as a value of
			// the wrong type several steps later.
			return errorAt(e.Pos, "select's branches are %s and %s; both must be one type", types[1], types[2])
		}
		node.Op = rule.OpSelect
		out = types[1]

	case "and", "or":
		if types[0] != rule.TypeBool || types[1] != rule.TypeBool {
			return errorAt(e.Pos, "%s takes BOOL and BOOL, got %s and %s", e.Op, types[0], types[1])
		}
		node.Op = map[string]rule.Op{"and": rule.OpAnd, "or": rule.OpOr}[e.Op]
		out = rule.TypeBool

	case "not":
		if types[0] != rule.TypeBool {
			return errorAt(e.Args[0].Pos, "not takes BOOL, got %s", types[0])
		}
		node.Op = rule.OpNot
		out = rule.TypeBool

	default:
		return errorAt(e.Pos, "%q is not an operation", e.Op)
	}

	if e.Policy != "" {
		policy, ok := c.policies[e.Policy]
		if !ok {
			return errorAt(e.PolicyPos, "policy %q is not declared", e.Policy)
		}
		p := policy
		node.Policy = &p
	}

	node.Type = out
	id, err := c.emitNode(r, s.Name, s.Pos, node)
	if err != nil {
		return err
	}
	scope[s.Name] = &binding{node: id, typ: out, pos: s.Pos}
	return nil
}

var comparisons = map[string]rule.Comparison{
	"eq": rule.CmpEq, "ne": rule.CmpNe, "lt": rule.CmpLt,
	"lte": rule.CmpLte, "gt": rule.CmpGt, "gte": rule.CmpGte,
}

// ---------------------------------------------------------------------------
// scope and node construction
// ---------------------------------------------------------------------------

func declare(scope map[string]*binding, name string, pos dsl.Position) error {
	if prior, dup := scope[name]; dup {
		// Names are bindings, not variables: rebinding one would make the DAG's
		// shape depend on statement order in a way the surface hides.
		return errorAt(pos, "%q is already bound at %s; a name is bound once", name, prior.pos)
	}
	scope[name] = nil
	return nil
}

func (c *compiler) resolve(r dsl.Rule, scope map[string]*binding, ref dsl.Ref) (*binding, error) {
	b, ok := scope[ref.Name]
	if !ok {
		return nil, errorAt(ref.Pos, "rule %q does not bind %q", r.Name, ref.Name)
	}
	if b == nil {
		// declare() reserves the name before the binding exists, so a self
		// reference lands here rather than resolving to a half-built node.
		return nil, errorAt(ref.Pos, "%q refers to itself", ref.Name)
	}
	b.used = true
	return b, nil
}

// emitNode appends a node and returns its identifier.
//
// The identifier is the rule name and the binding name joined by a dot, which
// makes it unique across the bundle and legible in a trace: an auditor reading
// `vat-standard.taxable` knows which rule produced the step without a lookup
// table (ADR-0005 §2.7).
func (c *compiler) emitNode(r dsl.Rule, local string, pos dsl.Position, n rule.Node) (rule.NodeID, error) {
	n.ID = rule.NodeID(r.Name + "." + local)
	if prior, dup := c.ids[n.ID]; dup {
		// Reachable without a duplicate binding name: two result slots in one
		// rule can slugify to one fragment. The runtime would refuse the bundle
		// at load; refusing it here names the two lines that collided.
		return "", errorAt(pos, "node %s is produced twice; the first is at %s", n.ID, prior)
	}
	c.ids[n.ID] = pos
	n.RuleVersion = r.Version
	n.RuleSemanticID = r.SemanticID
	c.nodes = append(c.nodes, n)
	return n.ID, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func errorAt(pos dsl.Position, format string, args ...any) error {
	return &dsl.Error{Pos: pos, Message: fmt.Sprintf(format, args...)}
}

func isSlug(s string) bool {
	if s == "" || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
}

// slugify renders a result slot as a node-identifier fragment. Slots are
// content vocabulary such as TAX_VAT, and the trace reads better with
// `vat-standard.emit-tax-vat` than with the raw slot.
func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func sortedNames(scope map[string]*binding) []string {
	out := make([]string, 0, len(scope))
	for name := range scope {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
