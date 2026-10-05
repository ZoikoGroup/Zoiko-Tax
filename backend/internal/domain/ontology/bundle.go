package ontology

import (
	"fmt"
	"sort"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
)

// BundlePart is one component of a commercial bundle, with the standalone
// value content or the customer supplied for it. A part with no standalone
// value cannot be allocated to, and classification will not invent one
// (ZTAX-CLS-REQ-0051).
type BundlePart struct {
	Component  ComponentInstance
	Standalone *fiscal.Money
	// Bundle is set when the part is itself a bundle.
	Bundle *Bundle
}

// Bundle is one commercial bundle line.
type Bundle struct {
	// LineRef and Amount are the original transaction line, preserved through
	// decomposition (ZTAX-CLS-REQ-0048).
	LineRef string
	Amount  fiscal.Money
	Parts   []BundlePart
	// Method and Provenance record how the allocation is made and on whose
	// authority (ZTAX-CLS-REQ-0049).
	Method     string
	Provenance string
	// Inseparable treats the bundle as one supply. It needs the country-pack
	// content that authorizes it (ZTAX-CLS-REQ-0047).
	Inseparable          bool
	InseparableAuthority string
}

// DecomposedLine is one atomic component's share of a bundle line.
type DecomposedLine struct {
	LineRef    string
	Path       []int
	Component  ComponentInstance
	Amount     fiscal.Money
	Method     string
	Provenance string
}

// Decompose splits a bundle line into atomic components before legal
// classification (ZTAX-CLS-REQ-0046), allocating its amount over the parts'
// standalone values by largest remainder. The parts always sum to the
// original amount exactly, every part carries the original line's reference,
// method and provenance, and nested bundles decompose recursively over an
// acyclic graph (ZTAX-CLS-REQ-0050).
func Decompose(b Bundle, p fiscal.RoundingPolicy) ([]DecomposedLine, error) {
	if err := acyclic(&b, map[*Bundle]bool{}); err != nil {
		return nil, err
	}
	return decompose(b, b.LineRef, nil, p)
}

func acyclic(b *Bundle, onPath map[*Bundle]bool) error {
	if onPath[b] {
		return fmt.Errorf("ontology: bundle %s contains itself", b.LineRef)
	}
	onPath[b] = true
	for _, part := range b.Parts {
		if part.Bundle != nil {
			if err := acyclic(part.Bundle, onPath); err != nil {
				return err
			}
		}
	}
	delete(onPath, b)
	return nil
}

func decompose(b Bundle, lineRef string, path []int, p fiscal.RoundingPolicy) ([]DecomposedLine, error) {
	if b.Method == "" || b.Provenance == "" {
		return nil, fmt.Errorf("ontology: bundle %s records no allocation method or provenance", b.LineRef)
	}
	if b.Inseparable {
		if b.InseparableAuthority == "" {
			return nil, fmt.Errorf("ontology: bundle %s is treated as inseparable with no country-pack authority", b.LineRef)
		}
		return nil, fmt.Errorf("ontology: bundle %s is inseparable under %s; it is classified whole, not decomposed", b.LineRef, b.InseparableAuthority)
	}
	if len(b.Parts) == 0 {
		return nil, fmt.Errorf("ontology: bundle %s has no parts", b.LineRef)
	}
	weights := make([]fiscal.Money, len(b.Parts))
	for i, part := range b.Parts {
		if part.Standalone == nil {
			return nil, fmt.Errorf("ontology: bundle %s part %d has no standalone value; classification does not invent an allocation", b.LineRef, i)
		}
		weights[i] = *part.Standalone
	}
	shares, err := fiscal.Allocate(b.Amount, weights, p)
	if err != nil {
		return nil, err
	}
	var out []DecomposedLine
	for i, part := range b.Parts {
		here := append(append([]int(nil), path...), i)
		if part.Bundle != nil {
			inner := *part.Bundle
			inner.Amount = shares[i]
			sub, err := decompose(inner, lineRef, here, p)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
			continue
		}
		out = append(out, DecomposedLine{
			LineRef: lineRef, Path: here, Component: part.Component, Amount: shares[i],
			Method: b.Method, Provenance: b.Provenance,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return lessPath(out[i].Path, out[j].Path) })
	return out, nil
}

func lessPath(a, b []int) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}
