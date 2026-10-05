package postgres_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// ADR-0004 §5.1 control 1: all accumulator lock acquisition routes through one
// sorting function, and a direct FOR UPDATE on accumulator_snapshot anywhere
// else is a build failure.
//
// The ADR says "lint". This is a test rather than a linter for the reason the
// rule exists at all: it has to fire on the first multi-accumulator rule
// written by someone who has not read the ADR (§3.4), and `go test ./...` is
// the gate every change already passes, with nothing to install or configure.
//
// What it inspects is SQL, not prose. Each non-test Go file under internal/
// and cmd/ is parsed, and its string literals — the only place a query can
// live, since every query in the adapter is a named const of literal SQL — are
// checked for the snapshot table and for a row-locking clause. Both in one
// file, anywhere but the repository, fails. Checking the file rather than each
// literal means a query split across a `+` is still caught; comments are not
// literals, so documentation that names the rule does not trip it.
//
// FOR SHARE and FOR KEY SHARE count too. They are weaker locks, and a shared
// lock taken out of canonical order deadlocks against an exclusive one exactly
// as well.

// lockingRepository is the one file allowed to lock a snapshot.
const lockingRepository = "internal/adapter/postgres/accumulator_repo.go"

var (
	snapshotTable = regexp.MustCompile(`(?i)\baccumulator_snapshot\b`)
	lockClause    = regexp.MustCompile(`(?i)\bfor\s+(no\s+key\s+update|update|key\s+share|share)\b`)
)

// locksSnapshot reports whether a Go source file's string literals both name
// the snapshot table and take a row lock.
func locksSnapshot(t *testing.T, name string, src []byte) bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	var table, lock bool
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			s = lit.Value
		}
		table = table || snapshotTable.MatchString(s)
		lock = lock || lockClause.MatchString(s)
		return true
	})
	return table && lock
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	// This package is internal/adapter/postgres; go test runs in its directory.
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found at %s: %v", root, err)
	}
	return root
}

func TestNoAccumulatorLockOutsideTheRepository(t *testing.T) {
	root := moduleRoot(t)
	var lockers []string
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || d.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if locksSnapshot(t, path, src) {
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				lockers = append(lockers, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// The scan has to see the one legitimate lock. If it does not, it is
	// blind — a renamed table, a changed clause, a broken walk — and passing
	// would mean nothing.
	if !slices.Contains(lockers, lockingRepository) {
		t.Fatalf("the scan did not find the snapshot lock in %s; it cannot be trusted to find one anywhere else", lockingRepository)
	}
	for _, f := range lockers {
		if f != lockingRepository {
			t.Errorf("%s locks accumulator_snapshot directly. Every accumulator lock goes through "+
				"AccumulatorRepository.LockAll, which takes them in accumulator.LockOrder (ADR-0004 §2.3, control 1).", f)
		}
	}
}

func TestLockScanSeesThroughConcatenationAndIgnoresComments(t *testing.T) {
	// The detector's own edges, so the gate above cannot pass by being
	// lenient.
	for name, tc := range map[string]struct {
		src  string
		want bool
	}{
		"one literal": {`package p
const q = "SELECT 1 FROM ztax.accumulator_snapshot WHERE k = $1 FOR UPDATE"`, true},
		"split across +": {`package p
const q = "SELECT 1 FROM ztax.accumulator_snapshot WHERE k = $1 " + "for   update"`, true},
		"raw string, other case": {"package p\nconst q = `select * from accumulator_snapshot\n\tFor No Key Update`", true},
		"share lock": {`package p
const q = "SELECT 1 FROM accumulator_snapshot FOR SHARE"`, true},
		"comment only": {`package p
// a direct FOR UPDATE on accumulator_snapshot is forbidden
const q = "SELECT 1"`, false},
		"other table": {`package p
const q = "SELECT 1 FROM ztax.outbox FOR UPDATE SKIP LOCKED"`, false},
		"unlocked read": {`package p
const q = "SELECT running_total FROM ztax.accumulator_snapshot WHERE k = $1"`, false},
	} {
		if got := locksSnapshot(t, name+".go", []byte(tc.src)); got != tc.want {
			t.Errorf("%s: locksSnapshot = %v, want %v", name, got, tc.want)
		}
	}
}
