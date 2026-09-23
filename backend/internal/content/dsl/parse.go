package dsl

import (
	"fmt"
	"strconv"
)

// Program is one source file: a bundle identity, an IR version, the rounding
// policies the file declares and the rules that use them.
//
// Positions are kept on everything a later stage can reject, because the later
// stage is the type checker and a type error with no line number is an error a
// content engineer has to bisect for. The AST is deliberately a plain data
// structure with no methods that interpret it — interpretation is
// internal/content/compile's job, and keeping the two apart is what lets the
// parser be tested against source text and the checker against an AST.
type Program struct {
	File      string
	BundleID  string
	IRVersion int
	Policies  []Policy
	Rules     []Rule

	BundlePos Position
	IRPos     Position
}

// Policy is a named rounding instruction, declared once and referenced by every
// step that rounds.
//
// ADR-0002 §2.2 gives rounding no default, so every APPLY_RATE and ROUND names
// one. Naming them at the top of a file rather than inline is not only brevity:
// it makes "which rounding does this pack use" a question a reviewer answers by
// reading five lines, rather than by auditing every arithmetic step.
type Policy struct {
	Name  string
	Mode  string
	Scale int
	Basis string
	Pos   Position
}

// Rule is one authored rule and the statements that make up its subgraph.
type Rule struct {
	// Name is a slug, unique within the bundle. It prefixes every node
	// identifier the rule produces, so a trace names the rule that produced a
	// step without a lookup.
	Name string
	// Version is the content version that authored this rule, and SemanticID
	// identifies its meaning across re-authorings (ADR-0012 §2.4). Both travel
	// onto every node (ADR-0005 §2.7).
	Version    string
	SemanticID string
	Statements []Statement

	Pos          Position
	VersionPos   Position
	SemanticPos  Position
	StatementEnd Position
}

// Statement is one line in a rule body.
type Statement interface{ Position() Position }

// ConstDecl declares a constant in the bundle's pool.
type ConstDecl struct {
	Name  string
	Type  string
	Value string
	// Qualifier is the currency, rate basis or unit, by keyword. Which keyword
	// is legal depends on Type and is checked by the parser, because
	// `const r rate "0.21" currency EUR` is a mistake worth catching where the
	// author can see both words.
	Qualifier string
	Pos       Position
}

// Position implements Statement.
func (d ConstDecl) Position() Position { return d.Pos }

// InputDecl binds a name to a field of the canonical input.
type InputDecl struct {
	Name  string
	Type  string
	Field string
	Pos   Position
}

// Position implements Statement.
func (d InputDecl) Position() Position { return d.Pos }

// AccumulatorDecl binds a name to an accumulator read before evaluation began
// (ADR-0005 §2.5). It is always MONEY; an accumulator over anything else has no
// meaning the threshold machinery in ZTAX-DET-001 §9 can use.
type AccumulatorDecl struct {
	Name string
	Key  string
	Pos  Position
}

// Position implements Statement.
func (d AccumulatorDecl) Position() Position { return d.Pos }

// LetStmt binds a name to the result of one operation.
type LetStmt struct {
	Name string
	Expr Expr
	Pos  Position
}

// Position implements Statement.
func (s LetStmt) Position() Position { return s.Pos }

// EmitStmt names a result slot and the binding that fills it. Emitted nodes are
// the bundle's roots.
type EmitStmt struct {
	Slot  string
	Value Ref
	Pos   Position
}

// Position implements Statement.
func (s EmitStmt) Position() Position { return s.Pos }

// RefuseStmt ends evaluation with a recorded reason rather than an error
// (ADR-0016 §2.1).
//
// A refusal has no arguments in IR version 1, so it is reached unconditionally
// and the whole bundle refuses. That is a real artifact — it is what a
// suspended country pack looks like — and the checker requires a rule
// containing one to contain nothing else, so it cannot be mistaken for a
// conditional refusal that silently swallows the rest of the file.
type RefuseStmt struct {
	Reason string
	Pos    Position
}

// Position implements Statement.
func (s RefuseStmt) Position() Position { return s.Pos }

// Ref is a reference to a name bound earlier in the same rule.
type Ref struct {
	Name string
	Pos  Position
}

// Expr is one operation over named bindings. There is no nesting: an operand is
// always a name, never another expression, which is what makes the AST and the
// DAG the same shape.
type Expr struct {
	Op   string
	Args []Ref
	// Comparison is the operator for `compare`.
	Comparison string
	// Policy names a declared rounding policy, for the operations that round.
	Policy string
	// Currency names a STRING constant holding the currency a per-unit rate
	// extends a quantity into, for `apply_rate` over a QUANTITY.
	Currency string

	Pos         Position
	PolicyPos   Position
	CurrencyPos Position
}

// Parse reads a source file into a Program. It reports the first error it
// finds: a content file that does not parse is not partially usable, and a list
// of cascading errors from one missing brace is noise.
func Parse(file, src string) (*Program, error) {
	toks, err := lex(file, src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	return p.program()
}

type parser struct {
	toks []token
	i    int
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tokenEOF {
		p.i++
	}
	return t
}

// word consumes an identifier and requires it to be exactly want.
func (p *parser) word(want string) error {
	t := p.next()
	if t.kind != tokenIdent || t.text != want {
		return errorf(t.pos, "expected %q, found %s", want, t.describe())
	}
	return nil
}

// ident consumes any identifier.
func (p *parser) ident(what string) (string, Position, error) {
	t := p.next()
	if t.kind != tokenIdent {
		return "", t.pos, errorf(t.pos, "expected %s, found %s", what, t.describe())
	}
	return t.text, t.pos, nil
}

// str consumes a quoted string.
func (p *parser) str(what string) (string, Position, error) {
	t := p.next()
	if t.kind != tokenString {
		return "", t.pos, errorf(t.pos, "expected %s as a quoted string, found %s", what, t.describe())
	}
	if t.text == "" {
		return "", t.pos, errorf(t.pos, "%s cannot be empty", what)
	}
	return t.text, t.pos, nil
}

func (p *parser) punct(want string) error {
	t := p.next()
	if t.kind != tokenPunct || t.text != want {
		return errorf(t.pos, "expected %q, found %s", want, t.describe())
	}
	return nil
}

func (p *parser) integer(what string) (int, Position, error) {
	t := p.next()
	if t.kind != tokenInt {
		return 0, t.pos, errorf(t.pos, "expected %s as a whole number, found %s", what, t.describe())
	}
	n, err := strconv.Atoi(t.text)
	if err != nil {
		return 0, t.pos, errorf(t.pos, "%s: %q is not a whole number this build can represent", what, t.text)
	}
	return n, t.pos, nil
}

func (p *parser) program() (*Program, error) {
	prog := &Program{File: p.peek().pos.File}

	if err := p.word("bundle"); err != nil {
		return nil, fmt.Errorf("a content file begins with a bundle declaration: %w", err)
	}
	id, pos, err := p.str("the bundle identity")
	if err != nil {
		return nil, err
	}
	prog.BundleID, prog.BundlePos = id, pos

	if err := p.word("ir"); err != nil {
		// The IR version is required rather than defaulted, because a bundle
		// that does not state which instruction set it was compiled for cannot
		// be refused by a runtime that predates it (ADR-0005 §2.8).
		return nil, fmt.Errorf("a bundle declares the IR version it compiles for: %w", err)
	}
	v, vpos, err := p.integer("the IR version")
	if err != nil {
		return nil, err
	}
	prog.IRVersion, prog.IRPos = v, vpos

	for {
		t := p.peek()
		switch {
		case t.kind == tokenEOF:
			if len(prog.Rules) == 0 {
				return nil, errorf(t.pos, "bundle %q declares no rules", prog.BundleID)
			}
			return prog, nil

		case t.kind == tokenIdent && t.text == "policy":
			pol, err := p.policy()
			if err != nil {
				return nil, err
			}
			prog.Policies = append(prog.Policies, pol)

		case t.kind == tokenIdent && t.text == "rule":
			r, err := p.rule()
			if err != nil {
				return nil, err
			}
			prog.Rules = append(prog.Rules, r)

		default:
			return nil, errorf(t.pos, "expected \"policy\" or \"rule\", found %s", t.describe())
		}
	}
}

// policy parses `policy line2 = HALF_UP scale 2 basis LINE`.
func (p *parser) policy() (Policy, error) {
	if err := p.word("policy"); err != nil {
		return Policy{}, err
	}
	name, pos, err := p.ident("a policy name")
	if err != nil {
		return Policy{}, err
	}
	if err := p.punct("="); err != nil {
		return Policy{}, err
	}
	mode, _, err := p.ident("a rounding mode")
	if err != nil {
		return Policy{}, err
	}
	if err := p.word("scale"); err != nil {
		return Policy{}, err
	}
	scale, _, err := p.integer("the scale")
	if err != nil {
		return Policy{}, err
	}
	if err := p.word("basis"); err != nil {
		return Policy{}, err
	}
	basis, _, err := p.ident("a rounding basis")
	if err != nil {
		return Policy{}, err
	}
	return Policy{Name: name, Mode: mode, Scale: scale, Basis: basis, Pos: pos}, nil
}

func (p *parser) rule() (Rule, error) {
	if err := p.word("rule"); err != nil {
		return Rule{}, err
	}
	name, pos, err := p.str("the rule name")
	if err != nil {
		return Rule{}, err
	}
	if err := p.word("version"); err != nil {
		return Rule{}, err
	}
	version, vpos, err := p.str("the rule version")
	if err != nil {
		return Rule{}, err
	}
	if err := p.word("semantic"); err != nil {
		return Rule{}, err
	}
	semantic, spos, err := p.str("the rule semantic id")
	if err != nil {
		return Rule{}, err
	}
	if err := p.punct("{"); err != nil {
		return Rule{}, err
	}

	r := Rule{Name: name, Version: version, SemanticID: semantic, Pos: pos, VersionPos: vpos, SemanticPos: spos}
	for {
		t := p.peek()
		if t.kind == tokenPunct && t.text == "}" {
			r.StatementEnd = t.pos
			p.next()
			if len(r.Statements) == 0 {
				return Rule{}, errorf(pos, "rule %q has no statements", name)
			}
			return r, nil
		}
		if t.kind == tokenEOF {
			return Rule{}, errorf(pos, "rule %q is never closed", name)
		}
		s, err := p.statement()
		if err != nil {
			return Rule{}, err
		}
		r.Statements = append(r.Statements, s)
	}
}

func (p *parser) statement() (Statement, error) {
	t := p.peek()
	if t.kind != tokenIdent {
		return nil, errorf(t.pos, "expected a statement, found %s", t.describe())
	}
	switch t.text {
	case "const":
		return p.constDecl()
	case "input":
		return p.inputDecl()
	case "accumulator":
		return p.accumulatorDecl()
	case "let":
		return p.letStmt()
	case "emit":
		return p.emitStmt()
	case "refuse":
		return p.refuseStmt()
	}
	return nil, errorf(t.pos, "%q is not a statement; expected const, input, accumulator, let, emit or refuse", t.text)
}

func (p *parser) constDecl() (Statement, error) {
	p.next()
	name, pos, err := p.ident("a constant name")
	if err != nil {
		return nil, err
	}
	typ, tpos, err := p.ident("a type")
	if err != nil {
		return nil, err
	}
	value, _, err := p.str("the constant's value")
	if err != nil {
		return nil, err
	}

	// Each type takes exactly one qualifier keyword, and the keyword is part of
	// the type rather than an option. ADR-0011 §2.1 P1 is why: a decimal string
	// with no currency, no basis and no unit is a number pretending to be a
	// fiscal quantity, and content is where that pretence would start.
	var qualifier string
	switch typ {
	case "money":
		if err := p.word("currency"); err != nil {
			return nil, fmt.Errorf("a money constant states its currency: %w", err)
		}
		qualifier, _, err = p.ident("a currency code")
	case "rate":
		if err := p.word("basis"); err != nil {
			return nil, fmt.Errorf("a rate constant states what it applies to (ZTAX-DET-001 §4.2): %w", err)
		}
		qualifier, _, err = p.ident("a rate basis")
	case "quantity":
		if err := p.word("unit"); err != nil {
			return nil, fmt.Errorf("a quantity constant states its unit: %w", err)
		}
		qualifier, _, err = p.ident("a unit")
	case "string", "reason":
		// No qualifier: neither carries a scale or a denomination.
	default:
		return nil, errorf(tpos, "%q is not a constant type; expected money, rate, quantity, string or reason", typ)
	}
	if err != nil {
		return nil, err
	}
	return ConstDecl{Name: name, Type: typ, Value: value, Qualifier: qualifier, Pos: pos}, nil
}

func (p *parser) inputDecl() (Statement, error) {
	p.next()
	name, pos, err := p.ident("an input name")
	if err != nil {
		return nil, err
	}
	typ, _, err := p.ident("a type")
	if err != nil {
		return nil, err
	}
	field, _, err := p.str("the canonical input path")
	if err != nil {
		return nil, err
	}
	return InputDecl{Name: name, Type: typ, Field: field, Pos: pos}, nil
}

func (p *parser) accumulatorDecl() (Statement, error) {
	p.next()
	name, pos, err := p.ident("an accumulator name")
	if err != nil {
		return nil, err
	}
	key, _, err := p.str("the accumulator key")
	if err != nil {
		return nil, err
	}
	return AccumulatorDecl{Name: name, Key: key, Pos: pos}, nil
}

func (p *parser) letStmt() (Statement, error) {
	p.next()
	name, pos, err := p.ident("a binding name")
	if err != nil {
		return nil, err
	}
	if err := p.punct("="); err != nil {
		return nil, err
	}
	e, err := p.expr()
	if err != nil {
		return nil, err
	}
	return LetStmt{Name: name, Expr: e, Pos: pos}, nil
}

func (p *parser) emitStmt() (Statement, error) {
	p.next()
	slot, pos, err := p.str("the result slot")
	if err != nil {
		return nil, err
	}
	value, vpos, err := p.ident("the binding to emit")
	if err != nil {
		return nil, err
	}
	return EmitStmt{Slot: slot, Value: Ref{Name: value, Pos: vpos}, Pos: pos}, nil
}

func (p *parser) refuseStmt() (Statement, error) {
	p.next()
	reason, pos, err := p.ident("a registered reason code")
	if err != nil {
		return nil, err
	}
	return RefuseStmt{Reason: reason, Pos: pos}, nil
}

// arities is the operand count of each operation, and it is the parser's only
// knowledge of what the operations mean. Types are the checker's business.
var arities = map[string]int{
	"add": 2, "sub": 2, "apply_rate": 2, "round": 1,
	"compare": 2, "select": 3, "and": 2, "or": 2, "not": 1,
}

func (p *parser) expr() (Expr, error) {
	op, pos, err := p.ident("an operation")
	if err != nil {
		return Expr{}, err
	}
	arity, known := arities[op]
	if !known {
		if op == "mul" || op == "quo" {
			// Naming them is kinder than "unknown operation", because an author
			// reaching for one has a calculation in mind and needs to know
			// which shape to use instead.
			return Expr{}, errorf(pos, "%q is not available in IR version 1: use apply_rate, so that the rounding policy is explicit (ADR-0005 §2.3); division awaits the inclusive-extraction semantics of ZTAX-DET-001 §7.4", op)
		}
		return Expr{}, errorf(pos, "%q is not an operation", op)
	}

	e := Expr{Op: op, Pos: pos}

	if op == "compare" {
		cmp, _, err := p.ident("a comparison operator")
		if err != nil {
			return Expr{}, err
		}
		e.Comparison = cmp
	}

	for n := 0; n < arity; n++ {
		name, npos, err := p.ident("an operand")
		if err != nil {
			return Expr{}, err
		}
		e.Args = append(e.Args, Ref{Name: name, Pos: npos})
	}

	if op == "apply_rate" {
		// Optional, and only meaningful over a QUANTITY. The checker rejects it
		// over a MONEY base, where the currency comes from the base itself and
		// naming a second one could only introduce a disagreement.
		if t := p.peek(); t.kind == tokenIdent && t.text == "currency" {
			p.next()
			cur, cpos, err := p.ident("a string constant naming the currency")
			if err != nil {
				return Expr{}, err
			}
			e.Currency, e.CurrencyPos = cur, cpos
		}
	}

	if op == "apply_rate" || op == "round" {
		if err := p.word("policy"); err != nil {
			return Expr{}, fmt.Errorf("%s rounds, so it names a rounding policy (ADR-0002 §2.2): %w", op, err)
		}
		name, ppos, err := p.ident("a declared policy name")
		if err != nil {
			return Expr{}, err
		}
		e.Policy, e.PolicyPos = name, ppos
	}

	return e, nil
}
