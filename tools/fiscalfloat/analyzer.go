// Package fiscalfloat implements ADR-0001 §5.1 control 1: binary floating-point
// must be unreachable from a Money, Rate or Quantity value object.
//
// Build Plan §8 rates binary floating-point on fiscal amounts as High risk, and
// the failure mode is silent — a float64 tax amount is still a plausible number.
// Review discipline does not catch this reliably, so it is a gate.
//
// Four rules, each closing a different route:
//
//	R1  No float declared anywhere in a guarded package. Guarded packages are the
//	    domain: if a float exists there at all, something fiscal can reach it.
//	R2  No conversion of a fiscal value to a float, anywhere in the module. This
//	    is the deliberate escape hatch, and it is the one to catch loudly.
//	R3  No call to a method on a fiscal type that returns a float, anywhere.
//	    (*apd.Decimal).Float64 is the specific case that motivates this.
//	R4  No struct holding both a fiscal field and a float field. Such a struct is
//	    a conversion waiting to be written, usually in a DTO or a log payload.
//
// R1 is the broad structural rule; R2, R3 and R4 close the routes that reach
// around it from packages that are not themselves guarded — transport, adapters,
// telemetry.
//
// Generated files are exempt from all four. The rules exist to constrain what
// someone writes in the domain, and the file `go test` generates to run a
// guarded package's own tests declares a *testing.M, which structurally
// contains a float. A gate that fires on machine-written code teaches people to
// pass -skip rather than to fix findings.
package fiscalfloat

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

const defaultFiscalTypes = "github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal.Money," +
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal.Rate," +
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal.Quantity," +
	"github.com/cockroachdb/apd/v3.Decimal"

const defaultGuardedPkgs = "github.com/zoikogroup/zoikotax/backend/internal/domain"

var (
	fiscalTypesFlag string
	guardedPkgsFlag string
)

// Analyzer is the go/analysis entry point.
var Analyzer = &analysis.Analyzer{
	Name:     "fiscalfloat",
	Doc:      "reports binary floating-point reachable from a fiscal value object (ADR-0001 §5.1 control 1)",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

func init() {
	Analyzer.Flags.StringVar(&fiscalTypesFlag, "fiscal-types", defaultFiscalTypes,
		"comma-separated fully qualified type names treated as fiscal value objects")
	Analyzer.Flags.StringVar(&guardedPkgsFlag, "guarded-pkgs", defaultGuardedPkgs,
		"comma-separated package path prefixes in which no float may be declared at all")
}

func run(pass *analysis.Pass) (any, error) {
	fiscal := splitList(fiscalTypesFlag)
	guarded := splitList(guardedPkgsFlag)

	c := &checker{
		pass:        pass,
		fiscalTypes: fiscal,
		guarded:     isGuarded(pass.Pkg.Path(), guarded),
		generated:   generatedFiles(pass),
	}

	// R1 — no float declared in a guarded package.
	//
	// Every declared object appears in Defs: vars, consts, named types, struct
	// fields, function parameters and results. Walking Defs rather than the AST
	// means a new syntactic way to declare something is covered automatically.
	if c.guarded {
		for id, obj := range pass.TypesInfo.Defs {
			if obj == nil || id == nil {
				continue
			}
			if obj.Pkg() != pass.Pkg || c.isGenerated(id.Pos()) {
				continue
			}
			if containsFloat(obj.Type(), make(map[types.Type]bool)) {
				pass.Reportf(id.Pos(),
					"%s declares binary floating-point in a guarded package; fiscal quantities are decimal (ADR-0001 §5.1 control 1)",
					describe(obj))
			}
		}
	}

	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	insp.Preorder([]ast.Node{
		(*ast.CallExpr)(nil),
		(*ast.StructType)(nil),
	}, func(n ast.Node) {
		switch node := n.(type) {
		case *ast.CallExpr:
			c.checkCall(node)
		case *ast.StructType:
			c.checkStruct(node)
		}
	})

	return nil, nil
}

type checker struct {
	pass        *analysis.Pass
	fiscalTypes []string
	guarded     bool
	generated   map[string]bool
}

// generatedFiles records the files carrying the generated-code marker, so that
// every rule can skip them.
func generatedFiles(pass *analysis.Pass) map[string]bool {
	out := make(map[string]bool)
	for _, file := range pass.Files {
		if ast.IsGenerated(file) {
			out[pass.Fset.Position(file.Pos()).Filename] = true
		}
	}
	return out
}

func (c *checker) isGenerated(pos token.Pos) bool {
	return c.generated[c.pass.Fset.Position(pos).Filename]
}

// checkCall covers R2 (conversion of a fiscal value to a float) and R3 (a method
// on a fiscal type returning a float).
func (c *checker) checkCall(call *ast.CallExpr) {
	if c.isGenerated(call.Pos()) {
		return
	}

	// R2 — a conversion is a call whose Fun denotes a type.
	if tv, ok := c.pass.TypesInfo.Types[call.Fun]; ok && tv.IsType() {
		if isFloat(tv.Type) && len(call.Args) == 1 {
			argType := c.pass.TypesInfo.TypeOf(call.Args[0])
			if argType != nil && c.containsFiscal(argType, make(map[types.Type]bool)) {
				c.pass.Reportf(call.Pos(),
					"conversion of fiscal value %s to %s; fiscal quantities never become binary floats (ADR-0001 §5.1 control 1)",
					argType.String(), tv.Type.String())
			}
		}
		return
	}

	// R3 — a method call whose receiver is fiscal and whose result is a float.
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	selection, ok := c.pass.TypesInfo.Selections[sel]
	if !ok {
		return
	}
	recv := selection.Recv()
	if recv == nil || !c.containsFiscal(recv, make(map[types.Type]bool)) {
		return
	}
	sig, ok := selection.Type().(*types.Signature)
	if !ok {
		return
	}
	results := sig.Results()
	for i := 0; i < results.Len(); i++ {
		if containsFloat(results.At(i).Type(), make(map[types.Type]bool)) {
			c.pass.Reportf(call.Pos(),
				"%s.%s returns binary floating-point from a fiscal type (ADR-0001 §5.1 control 1)",
				recv.String(), sel.Sel.Name)
			return
		}
	}
}

// checkStruct covers R4 — a struct holding both a fiscal field and a float field.
func (c *checker) checkStruct(st *ast.StructType) {
	if st.Fields == nil || c.isGenerated(st.Pos()) {
		return
	}
	var hasFiscal bool
	var floatField *ast.Field
	for _, field := range st.Fields.List {
		ft := c.pass.TypesInfo.TypeOf(field.Type)
		if ft == nil {
			continue
		}
		if c.containsFiscal(ft, make(map[types.Type]bool)) {
			hasFiscal = true
		}
		if floatField == nil && containsFloat(ft, make(map[types.Type]bool)) {
			floatField = field
		}
	}
	if hasFiscal && floatField != nil {
		c.pass.Reportf(floatField.Pos(),
			"struct holds both a fiscal value and binary floating-point; the conversion between them is a defect waiting to be written (ADR-0001 §5.1 control 1)")
	}
}

// containsFiscal reports whether t is, or structurally contains, a configured
// fiscal value object.
func (c *checker) containsFiscal(t types.Type, seen map[types.Type]bool) bool {
	return c.walk(t, seen, func(named *types.Named) bool {
		return c.isFiscalNamed(named)
	})
}

func (c *checker) isFiscalNamed(named *types.Named) bool {
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	qualified := obj.Pkg().Path() + "." + obj.Name()
	for _, want := range c.fiscalTypes {
		if qualified == want {
			return true
		}
	}
	return false
}

// walk applies match to every named type reachable from t, and returns true if
// any matches. It also descends through the structural composites so that a
// []Money or a map[string]Rate is caught.
func (c *checker) walk(t types.Type, seen map[types.Type]bool, match func(*types.Named) bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true

	switch u := t.(type) {
	case *types.Named:
		if match(u) {
			return true
		}
		// Methods on a fiscal type are reached through the receiver, not here;
		// descend into the underlying representation only.
		return c.walk(u.Underlying(), seen, match)
	case *types.Pointer:
		return c.walk(u.Elem(), seen, match)
	case *types.Slice:
		return c.walk(u.Elem(), seen, match)
	case *types.Array:
		return c.walk(u.Elem(), seen, match)
	case *types.Map:
		return c.walk(u.Key(), seen, match) || c.walk(u.Elem(), seen, match)
	case *types.Chan:
		return c.walk(u.Elem(), seen, match)
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if c.walk(u.Field(i).Type(), seen, match) {
				return true
			}
		}
	case *types.Signature:
		if p := u.Params(); p != nil {
			for i := 0; i < p.Len(); i++ {
				if c.walk(p.At(i).Type(), seen, match) {
					return true
				}
			}
		}
		if r := u.Results(); r != nil {
			for i := 0; i < r.Len(); i++ {
				if c.walk(r.At(i).Type(), seen, match) {
					return true
				}
			}
		}
	}
	return false
}

// containsFloat reports whether t is, or structurally contains, a binary float.
func containsFloat(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true

	if isFloat(t) {
		return true
	}

	switch u := t.(type) {
	case *types.Named:
		return containsFloat(u.Underlying(), seen)
	case *types.Pointer:
		return containsFloat(u.Elem(), seen)
	case *types.Slice:
		return containsFloat(u.Elem(), seen)
	case *types.Array:
		return containsFloat(u.Elem(), seen)
	case *types.Map:
		return containsFloat(u.Key(), seen) || containsFloat(u.Elem(), seen)
	case *types.Chan:
		return containsFloat(u.Elem(), seen)
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if containsFloat(u.Field(i).Type(), seen) {
				return true
			}
		}
	case *types.Signature:
		if p := u.Params(); p != nil {
			for i := 0; i < p.Len(); i++ {
				if containsFloat(p.At(i).Type(), seen) {
					return true
				}
			}
		}
		if r := u.Results(); r != nil {
			for i := 0; i < r.Len(); i++ {
				if containsFloat(r.At(i).Type(), seen) {
					return true
				}
			}
		}
	}
	return false
}

// isFloat reports whether t is a float32 or float64, including complex, whose
// components are binary floats and which has no business here either.
func isFloat(t types.Type) bool {
	basic, ok := t.Underlying().(*types.Basic)
	if !ok {
		return false
	}
	return basic.Info()&(types.IsFloat|types.IsComplex) != 0
}

func isGuarded(pkgPath string, prefixes []string) bool {
	for _, p := range prefixes {
		if pkgPath == p || strings.HasPrefix(pkgPath, p+"/") {
			return true
		}
	}
	return false
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func describe(obj types.Object) string {
	switch o := obj.(type) {
	case *types.Var:
		if o.IsField() {
			return "field " + o.Name()
		}
		return "declaration " + o.Name()
	case *types.Func:
		return "signature of " + o.Name()
	case *types.TypeName:
		return "type " + o.Name()
	case *types.Const:
		return "constant " + o.Name()
	}
	return obj.Name()
}
