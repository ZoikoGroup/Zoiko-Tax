// Command fiscalfloat is the standalone driver for the fiscalfloat analyzer.
//
//	go run ./cmd/fiscalfloat ../../...
//
// It exits non-zero on any finding, which is what makes it usable as a CI gate.
// ADR-0001 §5.1 control 1.
package main

import (
	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/zoikogroup/zoikotax/backend/tools/fiscalfloat"
)

func main() {
	singlechecker.Main(fiscalfloat.Analyzer)
}
