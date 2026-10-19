// Package archtest checks architecture-boundary rules that depguard cannot
// fully express, by looking at whole dependency chains instead of a single
// file's import list.
package archtest

import (
	"os/exec"
	"strings"
	"testing"
)

// ADR-0006 §2.6: there is no route from an AI suggestion to a fiscal record
// except through human review. backend/.golangci.yml enforces this with the
// depguard rule fiscal-never-imports-ai, but depguard only looks at the
// import statements written directly in the files it scans. If a domain
// package reached internal/domain/ai through some other package in
// between -- an import depguard never sees, because it is not in the
// protected file's own import list -- the lint rule would stay green while
// the thing it exists to stop had already happened.
//
// This test instead asks `go list -deps` for the full transitive dependency
// chain of every domain package. It is slower than depguard and it does not
// replace it: it exists to catch the one class of violation depguard
// structurally cannot see.
const aiImportPath = "github.com/zoikogroup/zoikotax/backend/internal/domain/ai"

// forbiddenAIDeps returns the lines of `go list` import-path output that
// name aiPath itself or anything below it. A pure function, no I/O, so the
// detector can be tested on its own in TestForbiddenAIDepsDetects.
func forbiddenAIDeps(goListOutput string, aiPath string) []string {
	var hits []string
	for _, line := range strings.Split(goListOutput, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == aiPath || strings.HasPrefix(line, aiPath+"/") {
			hits = append(hits, line)
		}
	}
	return hits
}

// moduleRoot finds the backend module's root directory without hardcoding a
// path, so the test works the same from any CWD inside the module.
func moduleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("go list -m -f {{.Dir}}: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// goList runs `go list -e <args>` with the module root as its working
// directory and returns its output, failing the test on error. -e makes a
// load error in one package (for example, an import cycle that a bad
// dependency on domain/ai would create) show up as an error on that
// package's own list entry, instead of aborting the whole pattern before
// any other package is even checked.
func goList(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", append([]string{"list", "-e"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -e %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestDomainNeverDependsOnAI(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not found on PATH; cannot run go list")
	}
	root := moduleRoot(t)

	pkgs := strings.Fields(goList(t, root, "./internal/domain/..."))
	if len(pkgs) == 0 {
		t.Fatal("go list ./internal/domain/... returned no packages; the module layout changed")
	}

	for _, pkg := range pkgs {
		if pkg == aiImportPath || strings.HasPrefix(pkg, aiImportPath+"/") {
			continue // the ai package does not need to avoid depending on itself
		}
		t.Run(pkg, func(t *testing.T) {
			deps := goList(t, root, "-deps", "-test", "-f", "{{.ImportPath}}", pkg)
			if hits := forbiddenAIDeps(deps, aiImportPath); len(hits) > 0 {
				t.Errorf("%s depends on internal/domain/ai via: %s. "+
					"domain packages must not depend on internal/domain/ai; see backend/.golangci.yml fiscal-never-imports-ai",
					pkg, strings.Join(hits, ", "))
			}
		})
	}
}

// The detector itself is tested, so a typo in the prefix/boundary check
// cannot make TestDomainNeverDependsOnAI pass vacuously.
func TestForbiddenAIDepsDetects(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"direct ai import", aiImportPath, 1},
		{"ai subpackage", aiImportPath + "/ids", 1},
		{"both direct and subpackage", aiImportPath + "\n" + aiImportPath + "/ids\n", 2},
		{
			"look-alikes only, no match",
			strings.Join([]string{
				aiImportPath + "xyz", // shares the prefix text but has no "/" boundary
				"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal",
			}, "\n"),
			0,
		},
		{"empty", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := forbiddenAIDeps(c.in, aiImportPath); len(got) != c.want {
				t.Errorf("forbiddenAIDeps(%q) = %v, want %d hit(s)", c.in, got, c.want)
			}
		})
	}
}

// TestGoListSeesAIDependency proves that `go list -deps`, as run on this
// machine, actually surfaces internal/domain/ai when a package genuinely
// depends on it. Without this, a passing TestDomainNeverDependsOnAI could
// mean the boundary holds, or it could mean go list silently found nothing
// at all -- a vacuous pass. internal/adapter/gateway is the chosen package
// because the architecture requires it to import internal/domain/ai (every
// AI call is routed through the gateway adapter), so the dependency is
// guaranteed, not incidental.
func TestGoListSeesAIDependency(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not found on PATH; cannot run go list")
	}
	root := moduleRoot(t)
	const gatewayPkg = "github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway"

	deps := goList(t, root, "-deps", "-test", "-f", "{{.ImportPath}}", gatewayPkg)
	if hits := forbiddenAIDeps(deps, aiImportPath); len(hits) == 0 {
		t.Fatalf("expected %s to depend on %s, but go list -deps reported none; "+
			"go list or the detector is not working on this machine", gatewayPkg, aiImportPath)
	}
}
