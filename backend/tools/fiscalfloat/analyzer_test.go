package fiscalfloat_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/zoikogroup/zoikotax/backend/tools/fiscalfloat"
)

// TestAnalyzer runs the four rules against fixtures.
//
// The fixtures declare their own stand-in fiscal types rather than importing the
// backend module, so this test proves the analyzer's logic without coupling the
// tools module to the code it inspects.
func TestAnalyzer(t *testing.T) {
	set := func(name, value string) {
		t.Helper()
		if err := fiscalfloat.Analyzer.Flags.Set(name, value); err != nil {
			t.Fatalf("set -%s: %v", name, err)
		}
	}
	set("fiscal-types", "fiscalfixture.Money,fiscalfixture.Rate")
	set("guarded-pkgs", "guarded")

	analysistest.Run(t, analysistest.TestData(), fiscalfloat.Analyzer, "guarded", "unguarded")
}
