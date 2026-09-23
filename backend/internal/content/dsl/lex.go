// Package dsl is the surface syntax of the rule language and its parser.
//
// ADR-0005 §2.1 puts authoring and compilation on the Z4 content plane: nothing
// here runs at request time, in the request path, or inside a regional cell.
// The depguard rule content-compiler-is-not-in-the-cell enforces that — no
// package under internal/app, internal/adapter, internal/transport or
// internal/domain may import this, and neither may cmd/ztax-core.
//
// The surface is deliberately small and deliberately boring. It has no
// expression nesting, no operator precedence and no control flow: every step is
// a named binding over previously named bindings, which is the rule DAG written
// out longhand. Three things follow, and each is worth more than the syntax
// sugar it costs:
//
//   - Shared subexpressions are visible. A taxable base feeding four taxes is
//     one name used four times, which is what ADR-0005 §3.1 says the DAG is for,
//     rather than something a compiler has to discover.
//   - Every node has a name a human chose, so the execution trace names it too
//     (ADR-0005 §2.7). An auditor reading a trace sees `vat-standard.taxable`,
//     not a synthesised identifier.
//   - The parser is small enough to read. A compiler bug here is a silent,
//     wide-blast-radius fiscal defect, which is why ADR-0005 §5.1 control 3
//     mandates mutation testing on it — and why there is no clever parsing.
//
// The grammar, in full:
//
//	program  := "bundle" STRING "ir" INT decl*
//	decl     := policy | rule
//	policy   := "policy" IDENT "=" IDENT "scale" INT "basis" IDENT
//	rule     := "rule" STRING "version" STRING "semantic" STRING "{" stmt* "}"
//	stmt     := const | input | accumulator | let | emit | refuse
//	const    := "const" IDENT "money"    STRING "currency" IDENT
//	          | "const" IDENT "rate"     STRING "basis" IDENT
//	          | "const" IDENT "quantity" STRING "unit" IDENT
//	          | "const" IDENT "string"   STRING
//	          | "const" IDENT "reason"   STRING
//	input    := "input" IDENT type STRING
//	accum    := "accumulator" IDENT STRING
//	let      := "let" IDENT "=" expr
//	emit     := "emit" STRING IDENT
//	refuse   := "refuse" IDENT
//	expr     := "add" IDENT IDENT
//	          | "sub" IDENT IDENT
//	          | "apply_rate" IDENT IDENT ["currency" IDENT] "policy" IDENT
//	          | "round" IDENT "policy" IDENT
//	          | "compare" IDENT IDENT IDENT
//	          | "select" IDENT IDENT IDENT
//	          | "and" IDENT IDENT
//	          | "or" IDENT IDENT
//	          | "not" IDENT
//
// Notably absent: multiplication and division. IR version 1 implements neither
// for Money — MUL is refused in favour of APPLY_RATE so that every product
// landing at a currency scale carries a rounding policy, and QUO awaits the
// inclusive-extraction semantics that ZTAX-DET-001 §7.4 specifies. A surface
// that could express them would compile bundles the evaluator refuses at
// request time, which is the worst place to discover it.
package dsl

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Position is a source location, one-based, for error messages. Content
// engineers are the audience for every error this package produces, and an
// error without a line number is an error somebody has to bisect for.
type Position struct {
	File   string
	Line   int
	Column int
}

// String renders file:line:col.
func (p Position) String() string {
	if p.File == "" {
		return fmt.Sprintf("%d:%d", p.Line, p.Column)
	}
	return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Column)
}

// tokenKind is the closed set of lexical categories.
type tokenKind uint8

const (
	tokenEOF tokenKind = iota
	tokenIdent
	tokenString
	tokenInt
	tokenPunct
)

func (k tokenKind) String() string {
	switch k {
	case tokenEOF:
		return "end of file"
	case tokenIdent:
		return "a word"
	case tokenString:
		return "a quoted string"
	case tokenInt:
		return "a whole number"
	case tokenPunct:
		return "punctuation"
	}
	return "something unrecognised"
}

// token is one lexeme.
type token struct {
	kind tokenKind
	text string
	pos  Position
}

func (t token) describe() string {
	if t.kind == tokenEOF {
		return "end of file"
	}
	return fmt.Sprintf("%q", t.text)
}

// Error is a syntax or semantic failure with a source position.
//
// It is a distinct type rather than a wrapped fmt.Errorf so that a future
// authoring environment can underline the offending span without parsing a
// message — ADR-0005 §5.2 lists tooling for content engineers as a deliverable,
// not an afterthought.
type Error struct {
	Pos     Position
	Message string
}

// Error implements error.
func (e *Error) Error() string { return e.Pos.String() + ": " + e.Message }

func errorf(pos Position, format string, args ...any) *Error {
	return &Error{Pos: pos, Message: fmt.Sprintf(format, args...)}
}

// lex splits source into tokens.
//
// It is a single pass with no lookahead beyond one rune. Comments run from # to
// the end of the line; newlines are whitespace, so a statement may be wrapped
// without a continuation marker. Strings are double-quoted and carry no escape
// sequences at all: every string in this language is a decimal literal, a field
// path, a slug or a reason code, and none of those contains a quote. Rejecting
// an escape outright is cheaper than implementing one nobody should use, and it
// keeps the digest of a compiled bundle independent of how its source was
// written.
func lex(file, src string) ([]token, error) {
	var (
		toks   []token
		line   = 1
		col    = 1
		offset = 0
	)
	at := func() Position { return Position{File: file, Line: line, Column: col} }

	advance := func(r rune, size int) {
		offset += size
		if r == '\n' {
			line++
			col = 1
			return
		}
		col++
	}

	for offset < len(src) {
		r, size := utf8.DecodeRuneInString(src[offset:])

		switch {
		case r == '#':
			for offset < len(src) {
				c, s := utf8.DecodeRuneInString(src[offset:])
				if c == '\n' {
					break
				}
				advance(c, s)
			}

		case unicode.IsSpace(r):
			advance(r, size)

		case r == '"':
			start := at()
			advance(r, size)
			var b strings.Builder
			closed := false
			for offset < len(src) {
				c, s := utf8.DecodeRuneInString(src[offset:])
				if c == '\n' {
					break
				}
				advance(c, s)
				if c == '"' {
					closed = true
					break
				}
				if c == '\\' {
					return nil, errorf(start, "a string in this language carries no escape sequences; remove the backslash")
				}
				b.WriteRune(c)
			}
			if !closed {
				return nil, errorf(start, "this string is never closed")
			}
			toks = append(toks, token{kind: tokenString, text: b.String(), pos: start})

		case r == '{' || r == '}' || r == '=':
			toks = append(toks, token{kind: tokenPunct, text: string(r), pos: at()})
			advance(r, size)

		case r >= '0' && r <= '9':
			start := at()
			var b strings.Builder
			for offset < len(src) {
				c, s := utf8.DecodeRuneInString(src[offset:])
				if c < '0' || c > '9' {
					break
				}
				b.WriteRune(c)
				advance(c, s)
			}
			toks = append(toks, token{kind: tokenInt, text: b.String(), pos: start})

		case isIdentStart(r):
			start := at()
			var b strings.Builder
			for offset < len(src) {
				c, s := utf8.DecodeRuneInString(src[offset:])
				if !isIdentRune(c) {
					break
				}
				b.WriteRune(c)
				advance(c, s)
			}
			toks = append(toks, token{kind: tokenIdent, text: b.String(), pos: start})

		default:
			return nil, errorf(at(), "%q has no meaning in this language", string(r))
		}
	}

	toks = append(toks, token{kind: tokenEOF, pos: Position{File: file, Line: line, Column: col}})
	return toks, nil
}

// isIdentStart and isIdentRune keep identifiers ASCII.
//
// Unicode identifiers would be friendlier to non-English content teams and
// would also make two visually identical names distinct, which in a document
// that decides a tax liability is a defect waiting for a homoglyph. Field paths
// and reason codes are strings, where Unicode is fine, because those are
// compared as data rather than resolved as names.
func isIdentStart(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func isIdentRune(r rune) bool {
	return isIdentStart(r) || (r >= '0' && r <= '9') || r == '-'
}
