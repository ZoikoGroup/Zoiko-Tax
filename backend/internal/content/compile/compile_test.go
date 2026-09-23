package compile_test

import (
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/content/bundle"
	"github.com/zoikogroup/zoikotax/backend/internal/content/compile"
	"github.com/zoikogroup/zoikotax/backend/internal/content/dsl"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
)

const vatSource = `
bundle "eu-vat-2026.09"
ir 1

policy line2 = HALF_UP scale 2 basis LINE

rule "vat-standard" version "2026.09.1" semantic "ZTAX-RULE-VAT-STANDARD" {
    const zero     money "0.00"   currency EUR
    const standard rate  "0.2100" basis NET

    input net    money "line.netAmount"
    input exempt bool  "line.exemptCertificateHeld"

    let vat     = apply_rate net standard policy line2
    let payable = select exempt zero vat

    emit "TAX_VAT" payable
}
`

func compileSource(t *testing.T, src string) rule.Manifest {
	t.Helper()
	prog, err := dsl.Parse("test.ztax", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m, err := compile.Compile(prog)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return m
}

// TestCompiledBundleEvaluates is the end-to-end claim the compiler exists to
// make: source text becomes a manifest, the manifest loads under the runtime's
// own verification, and evaluating it produces the figure the rule describes.
// Anything less and the compiler is emitting artifacts nobody can run.
func TestCompiledBundleEvaluates(t *testing.T) {
	m := compileSource(t, vatSource)
	m.Digest = "zt1:0000000000000000000000000000000000000000000000000000000000000000"

	b, err := rule.Load(m)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	net, err := fiscal.ParseMoney("100.00", "EUR")
	if err != nil {
		t.Fatalf("parse money: %v", err)
	}

	for _, tc := range []struct {
		name   string
		exempt bool
		want   string
	}{
		{name: "taxable", exempt: false, want: "21.00"},
		{name: "exempt", exempt: true, want: "0.00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rule.Evaluate(b, rule.Frame{
				Money: map[string]fiscal.Money{"line.netAmount": net},
				Flags: map[string]bool{"line.exemptCertificateHeld": tc.exempt},
			})
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			v, ok := got.Emitted["TAX_VAT"]
			if !ok {
				t.Fatalf("no TAX_VAT in %v", got.Emitted)
			}
			if v.Money.CanonicalString() != tc.want {
				t.Errorf("TAX_VAT = %s, want %s", v.Money.CanonicalString(), tc.want)
			}
		})
	}
}

// TestTraceNamesTheRule checks ADR-0005 §2.7: every step carries the rule
// version and semantic id that authored it, or a decision cannot say which rule
// produced which figure.
func TestTraceNamesTheRule(t *testing.T) {
	m := compileSource(t, vatSource)
	m.Digest = "zt1:0000000000000000000000000000000000000000000000000000000000000000"
	b, err := rule.Load(m)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	net, _ := fiscal.ParseMoney("100.00", "EUR")
	got, err := rule.Evaluate(b, rule.Frame{
		Money: map[string]fiscal.Money{"line.netAmount": net},
		Flags: map[string]bool{"line.exemptCertificateHeld": false},
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(got.Trace) == 0 {
		t.Fatal("no trace")
	}
	for _, step := range got.Trace {
		if step.RuleVersion != "2026.09.1" || step.RuleSemanticID != "ZTAX-RULE-VAT-STANDARD" {
			t.Errorf("step %s carries %q/%q", step.Node, step.RuleVersion, step.RuleSemanticID)
		}
		if !strings.HasPrefix(string(step.Node), "vat-standard.") {
			t.Errorf("step %s is not named for its rule", step.Node)
		}
	}
}

// TestCompilationIsReproducible is C3 at the compiler. Two compilations of one
// source must produce identical bytes, or the bundle digest — and therefore
// every replay that names it — depends on which machine compiled it.
func TestCompilationIsReproducible(t *testing.T) {
	first, err := bundle.EncodeManifest(compileSource(t, vatSource))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for i := 0; i < 32; i++ {
		again, err := bundle.EncodeManifest(compileSource(t, vatSource))
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("compilation %d differs:\n%s\n%s", i, first, again)
		}
	}
}

// TestManifestRoundTrip proves the on-disk form is exactly what the compiler
// wrote: decode checks the bytes are canonical, so a pass here means a cell
// reading this file digests what the signer signed.
func TestManifestRoundTrip(t *testing.T) {
	m := compileSource(t, vatSource)
	data, err := bundle.EncodeManifest(m)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	back, digest, err := bundle.DecodeManifest(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if digest.IsZero() {
		t.Fatal("no digest")
	}
	if back.BundleID != m.BundleID || len(back.Nodes) != len(m.Nodes) || len(back.Constants) != len(m.Constants) {
		t.Fatalf("round trip lost content: %d/%d nodes, %d/%d constants",
			len(back.Nodes), len(m.Nodes), len(back.Constants), len(m.Constants))
	}
	if back.Digest != "" {
		t.Error("the manifest file carries a digest field; it belongs in the seal")
	}
}

// TestRefusals is the compiler's half of the bargain in the package comment:
// nothing it emits may fail at evaluation for a reason compilation could have
// found. Each case here is a mistake a content author will make, and each names
// the line it is on.
func TestRefusals(t *testing.T) {
	cases := []struct {
		name string
		// old and replacement edit the working fixture, so each case reads as the
		// one thing it changes rather than as a second copy of the source.
		old, replacement string
		want             string
	}{
		{
			name:        "rate applied without a policy",
			old:         "let vat     = apply_rate net standard policy line2",
			replacement: "let vat     = apply_rate net standard",
			want:        "rounding policy",
		},
		{
			name:        "policy that was never declared",
			old:         "policy line2 = HALF_UP scale 2 basis LINE",
			replacement: "policy doc2 = HALF_UP scale 2 basis LINE",
			want:        `policy "line2" is not declared`,
		},
		{
			name:        "rate where money was wanted",
			old:         "let payable = select exempt zero vat",
			replacement: "let payable = select exempt standard vat",
			want:        "both must be one type",
		},
		{
			name:        "applying something that is not a rate",
			old:         "let vat     = apply_rate net standard policy line2",
			replacement: "let vat     = apply_rate net net policy line2",
			want:        "takes a RATE as its second operand",
		},
		{
			name: "ordering two booleans",
			old:  "let payable = select exempt zero vat",
			replacement: `let cmp     = compare gt exempt exempt
    let payable = select cmp zero vat`,
			want: "only eq and ne are defined",
		},
		{
			name:        "reference to a name nothing binds",
			old:         "let payable = select exempt zero vat",
			replacement: "let payable = select exempt zero total",
			want:        `does not bind "total"`,
		},
		{
			name: "binding that nothing uses",
			old:  `    emit "TAX_VAT" payable`,
			replacement: `    let spare   = add vat vat
    emit "TAX_VAT" payable`,
			want: "never uses it",
		},
		{
			name:        "multiplication, which IR version 1 does not implement",
			old:         "let vat     = apply_rate net standard policy line2",
			replacement: "let vat     = mul net standard",
			want:        "use apply_rate",
		},
		{
			name: "an unregistered reason code",
			old:  `    emit "TAX_VAT" payable`,
			replacement: `    emit "TAX_VAT" payable
    refuse NOT_A_REAL_CODE`,
			want: "not a registered reason code",
		},
		{
			name:        "a rate with no basis",
			old:         `const standard rate  "0.2100" basis NET`,
			replacement: `const standard rate  "0.2100" basis WHATEVER`,
			want:        "rate basis",
		},
		{
			name:        "a rounding mode that does not exist",
			old:         "policy line2 = HALF_UP scale 2 basis LINE",
			replacement: "policy line2 = HALF_SIDEWAYS scale 2 basis LINE",
			want:        "unknown rounding mode",
		},
		{
			name:        "a scale beyond what a policy may request",
			old:         "policy line2 = HALF_UP scale 2 basis LINE",
			replacement: "policy line2 = HALF_UP scale 40 basis LINE",
			want:        "out of range",
		},
		{
			name:        "an IR version this compiler does not emit",
			old:         "ir 1",
			replacement: "ir 2",
			want:        "IR version 1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if n := strings.Count(vatSource, tc.old); n != 1 {
				// A fixture that drifted out from under a case is a case that
				// silently stopped testing anything.
				t.Fatalf("the fixture contains %q %d times, want once", tc.old, n)
			}
			src := strings.Replace(vatSource, tc.old, tc.replacement, 1)

			prog, err := dsl.Parse("test.ztax", src)
			if err == nil {
				_, err = compile.Compile(prog)
			}
			if err == nil {
				t.Fatal("compiled; expected a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "test.ztax:") {
				t.Errorf("error %q names no source position", err)
			}
		})
	}
}
