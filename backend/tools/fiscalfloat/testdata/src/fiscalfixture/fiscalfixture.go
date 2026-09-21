// Package fiscalfixture stands in for internal/domain/fiscal in the analyzer's
// tests. Keeping the fixture local means the tools module never imports the
// backend module it inspects.
package fiscalfixture

// Money has an integral underlying type purely so that a conversion to float64
// type-checks — the R2 fixture needs a conversion that compiles in order to
// prove the analyzer rejects it. The real Money is a struct with no such route.
type Money int64

// Rate is the second configured fiscal type.
type Rate int64

// Float64 is the shape of the escape hatch R3 exists to catch:
// (*apd.Decimal).Float64 is the real-world case.
func (m Money) Float64() float64 {
	var f float64
	return f
}

// Text is the safe accessor: fiscal values leave as strings.
func (m Money) Text() string { return "0" }
