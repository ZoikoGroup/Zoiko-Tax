package content_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
)

func node(id string, version string, level content.Level, deps ...string) content.PackNode {
	n := content.PackNode{ID: content.PackID(id), Version: v(version), Level: level}
	for i := 0; i+1 < len(deps); i += 2 {
		n.Dependencies = append(n.Dependencies, content.Dependency{Pack: content.PackID(deps[i]), Constraint: content.MustConstraint(deps[i+1])})
	}
	return n
}

func ids(ns []content.PackNode) []content.PackID {
	out := make([]content.PackID, len(ns))
	for i, n := range ns {
		out[i] = n.ID
	}
	return out
}

func failureOf(t *testing.T, err error) content.ResolutionFailure {
	t.Helper()
	var re *content.ResolutionError
	if !errors.As(err, &re) {
		t.Fatalf("got %v, want a ResolutionError", err)
	}
	return re.Failure
}

func TestResolveOrdersDependenciesFirst(t *testing.T) {
	packs := []content.PackNode{
		node("OVERLAY-A", "1.0.0", content.LevelCustomerOverlay, "NAT-X", "^2.1"),
		node("NAT-X", "2.3.0", content.LevelNational, "REG-EU", "^1", "GLOBAL-COMMON", "~1.4"),
		node("REG-EU", "1.0.4", content.LevelRegional, "GLOBAL-COMMON", ">=1.4.0 <2"),
		node("GLOBAL-COMMON", "1.4.2", content.LevelGlobal),
		node("UNRELATED", "1.0.0", content.LevelNational),
	}
	got, err := content.Resolve(packs, "OVERLAY-A")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := []content.PackID{"GLOBAL-COMMON", "REG-EU", "NAT-X", "OVERLAY-A"}
	if !reflect.DeepEqual(ids(got), want) {
		t.Errorf("order %v, want %v", ids(got), want)
	}

	all, err := content.Resolve(packs)
	if err != nil {
		t.Fatalf("resolve all: %v", err)
	}
	// UNRELATED is ready at the start, but GLOBAL and REGIONAL break the tie
	// by level before it.
	wantAll := []content.PackID{"GLOBAL-COMMON", "REG-EU", "NAT-X", "UNRELATED", "OVERLAY-A"}
	if !reflect.DeepEqual(ids(all), wantAll) {
		t.Errorf("order %v, want %v", ids(all), wantAll)
	}
}

func TestResolveRefuses(t *testing.T) {
	cases := []struct {
		name  string
		packs []content.PackNode
		root  string
		want  content.ResolutionFailure
	}{
		{"a missing dependency", []content.PackNode{
			node("NAT-X", "1.0.0", content.LevelNational, "REG-EU", "^1"),
		}, "NAT-X", content.FailureMissing},
		{"an unsatisfied version", []content.PackNode{
			node("NAT-X", "1.0.0", content.LevelNational, "REG-EU", "^2"),
			node("REG-EU", "1.9.0", content.LevelRegional),
		}, "NAT-X", content.FailureUnsatisfied},
		{"national depending on a customer overlay", []content.PackNode{
			node("NAT-X", "1.0.0", content.LevelNational, "OVERLAY-A", "^1"),
			node("OVERLAY-A", "1.0.0", content.LevelCustomerOverlay),
		}, "NAT-X", content.FailureLevel},
		{"global depending on regional", []content.PackNode{
			node("GLOBAL-COMMON", "1.0.0", content.LevelGlobal, "REG-EU", "^1"),
			node("REG-EU", "1.0.0", content.LevelRegional),
		}, "GLOBAL-COMMON", content.FailureLevel},
		{"a cycle within one level", []content.PackNode{
			node("NAT-A", "1.0.0", content.LevelNational, "NAT-B", "^1"),
			node("NAT-B", "1.0.0", content.LevelNational, "NAT-C", "^1"),
			node("NAT-C", "1.0.0", content.LevelNational, "NAT-A", "^1"),
		}, "NAT-A", content.FailureCycle},
		{"a self dependency", []content.PackNode{
			node("NAT-A", "1.0.0", content.LevelNational, "NAT-A", "^1"),
		}, "NAT-A", content.FailureCycle},
		{"two versions of one pack", []content.PackNode{
			node("NAT-A", "1.0.0", content.LevelNational),
			node("NAT-A", "1.1.0", content.LevelNational),
		}, "NAT-A", content.FailureDuplicate},
		{"an unknown root", []content.PackNode{node("NAT-A", "1.0.0", content.LevelNational)}, "NAT-B", content.FailureUnknownRoot},
		{"a declared conflict", []content.PackNode{
			{ID: "NAT-A", Version: v("1.0.0"), Level: content.LevelNational,
				Dependencies: []content.Dependency{{Pack: "REG-EU", Constraint: content.MustConstraint("^1")}},
				Conflicts:    []content.Conflict{{Pack: "GLOBAL-OLD", Constraint: content.MustConstraint("<2")}}},
			node("REG-EU", "1.0.0", content.LevelRegional, "GLOBAL-OLD", "^1"),
			node("GLOBAL-OLD", "1.5.0", content.LevelGlobal),
		}, "NAT-A", content.FailureConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := content.Resolve(tc.packs, content.PackID(tc.root))
			if err == nil {
				t.Fatal("resolved")
			}
			if got := failureOf(t, err); got != tc.want {
				t.Errorf("failure %s, want %s (%v)", got, tc.want, err)
			}
		})
	}
}

func TestResolveNamesTheCycle(t *testing.T) {
	_, err := content.Resolve([]content.PackNode{
		node("NAT-A", "1.0.0", content.LevelNational, "NAT-B", "^1"),
		node("NAT-B", "1.0.0", content.LevelNational, "NAT-A", "^1"),
	})
	if err == nil || !strings.Contains(err.Error(), "NAT-A → NAT-B → NAT-A") {
		t.Fatalf("got %v, want the cycle named", err)
	}
}

// genAcyclic draws a set of packs whose dependency graph is a DAG that obeys
// the level rule: packs are generated most general first, and each depends
// only on earlier packs at its own level or a more general one, with a
// constraint that admits the dependency's version.
func genAcyclic(t *rapid.T) []content.PackNode {
	n := rapid.IntRange(1, 14).Draw(t, "packs")
	packs := make([]content.PackNode, n)
	level := 0
	for i := range packs {
		level = rapid.IntRange(level, len(content.Levels)-1).Draw(t, fmt.Sprintf("level%d", i))
		ver := content.Version{
			Major: rapid.IntRange(0, 3).Draw(t, fmt.Sprintf("major%d", i)),
			Minor: rapid.IntRange(0, 5).Draw(t, fmt.Sprintf("minor%d", i)),
			Patch: rapid.IntRange(0, 5).Draw(t, fmt.Sprintf("patch%d", i)),
		}
		if ver.IsZero() {
			ver.Patch = 1
		}
		packs[i] = content.PackNode{ID: content.PackID(fmt.Sprintf("P%02d", i)), Version: ver, Level: content.Levels[level]}
		for j := 0; j < i; j++ {
			if !rapid.Bool().Draw(t, fmt.Sprintf("edge%d-%d", i, j)) {
				continue
			}
			dv := packs[j].Version
			c := rapid.SampledFrom([]string{
				"=" + dv.String(),
				"^" + dv.String(),
				"~" + dv.String(),
				">=" + dv.String(),
			}).Draw(t, fmt.Sprintf("constraint%d-%d", i, j))
			packs[i].Dependencies = append(packs[i].Dependencies, content.Dependency{Pack: packs[j].ID, Constraint: content.MustConstraint(c)})
		}
	}
	return packs
}

// TestResolvePropertyTopologicalAndOrderIndependent is the determinism claim
// of ZTAX-CONT-REQ-0008 as a property: for any valid pack set, the resolved
// order puts every dependency before its dependent, and shuffling the input
// does not change the output by a single position.
func TestResolvePropertyTopologicalAndOrderIndependent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		packs := genAcyclic(t)
		got, err := content.Resolve(packs)
		if err != nil {
			t.Fatalf("a valid DAG did not resolve: %v", err)
		}
		if len(got) != len(packs) {
			t.Fatalf("resolved %d of %d packs", len(got), len(packs))
		}
		pos := map[content.PackID]int{}
		for i, n := range got {
			pos[n.ID] = i
		}
		for _, n := range got {
			for _, d := range n.Dependencies {
				if pos[d.Pack] >= pos[n.ID] {
					t.Fatalf("%s is loaded at %d, before its dependency %s at %d", n.ID, pos[n.ID], d.Pack, pos[d.Pack])
				}
			}
		}

		shuffled := rapid.Permutation(packs).Draw(t, "shuffled")
		again, err := content.Resolve(shuffled)
		if err != nil {
			t.Fatalf("the same set, reordered, did not resolve: %v", err)
		}
		if !reflect.DeepEqual(ids(got), ids(again)) {
			t.Fatalf("input order leaked into the result:\n  %v\n  %v", ids(got), ids(again))
		}
	})
}

// TestResolvePropertyBackEdgeIsRefused closes any generated DAG with one edge
// from a pack to one of its own dependents' ancestors — at the same level, so
// that the level rule cannot be what catches it — and requires a refusal.
func TestResolvePropertyBackEdgeIsRefused(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		packs := genAcyclic(t)
		// Find an edge i → j at one level and add j → i.
		for i := range packs {
			for _, d := range packs[i].Dependencies {
				j := -1
				for k := range packs {
					if packs[k].ID == d.Pack {
						j = k
					}
				}
				if packs[j].Level != packs[i].Level {
					continue
				}
				packs[j].Dependencies = append(packs[j].Dependencies,
					content.Dependency{Pack: packs[i].ID, Constraint: content.MustConstraint(">=0.0.0")})
				_, err := content.Resolve(packs)
				if err == nil {
					t.Fatalf("a cycle %s ⇄ %s resolved", packs[i].ID, packs[j].ID)
				}
				var re *content.ResolutionError
				if !errors.As(err, &re) || re.Failure != content.FailureCycle {
					t.Fatalf("got %v, want CYCLE", err)
				}
				return
			}
		}
	})
}
