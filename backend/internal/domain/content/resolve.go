package content

import (
	"fmt"
	"sort"
	"strings"
)

// PackNode is one pack as dependency resolution sees it.
type PackNode struct {
	ID           PackID
	Version      Version
	Level        Level
	Dependencies []Dependency
	Conflicts    []Conflict
}

// ResolutionFailure classifies why a set of packs does not resolve.
type ResolutionFailure string

// The failures.
const (
	FailureDuplicate    ResolutionFailure = "DUPLICATE_PACK"
	FailureUnknownRoot  ResolutionFailure = "UNKNOWN_ROOT"
	FailureMissing      ResolutionFailure = "MISSING_DEPENDENCY"
	FailureUnsatisfied  ResolutionFailure = "UNSATISFIED_VERSION"
	FailureLevel        ResolutionFailure = "LEVEL_INVERSION"
	FailureConflict     ResolutionFailure = "CONFLICT"
	FailureCycle        ResolutionFailure = "CYCLE"
	FailureInvalidLevel ResolutionFailure = "INVALID_LEVEL"
	FailureNoConstraint ResolutionFailure = "NO_CONSTRAINT"
)

// ResolutionError is a refusal to resolve, with the pack it is about.
//
// It is a type rather than a string so that the build plane and, later, the
// content service can report "which pack, which failure" without parsing a
// message — the same reason dsl.Error carries a position.
type ResolutionError struct {
	Failure ResolutionFailure
	Pack    PackID
	Detail  string
}

// Error implements error.
func (e *ResolutionError) Error() string {
	return fmt.Sprintf("content: pack %s: %s: %s", e.Pack, e.Failure, e.Detail)
}

func failure(f ResolutionFailure, p PackID, format string, args ...any) error {
	return &ResolutionError{Failure: f, Pack: p, Detail: fmt.Sprintf(format, args...)}
}

// Resolve checks a set of packs composes and returns them in load order:
// every pack after everything it depends on (ZTAX-CONT-REQ-0008,
// ZTAX-CONT-REQ-0087).
//
// available is the set of packs that exist — for a build, every pack in the
// source tree; for a cell, every pack it holds. It holds **one version per
// pack identity**. A set offering two versions of one pack is refused rather
// than resolved by picking one, for the same reason the cell's loader refuses a
// directory holding two bundles: any choice made here would be a guess about
// which law to apply, and version pinning exists so that nobody migrates to a
// different version silently (ZTAX-CONT-REQ-0089). Choosing a version is a
// release decision, made by whoever assembles the set.
//
// roots names the packs being resolved; their transitive dependencies are
// pulled from available, and packs nothing reaches are left out. With no roots
// the whole set is resolved.
//
// Within the resolved closure, every one of these is refused:
//
//   - a dependency on a pack that is not available        MISSING_DEPENDENCY
//   - a dependency whose version constraint is not met    UNSATISFIED_VERSION
//   - a dependency on a less general level                LEVEL_INVERSION
//   - two packs that declare a conflict with each other   CONFLICT
//   - any cycle, including a pack depending on itself     CYCLE
//
// The order is deterministic and independent of the order of available: ties
// between packs that are ready at once break by level (most general first),
// then by identity. That is Kahn's algorithm with a sorted ready set, the same
// construction rule.Load uses for the rule DAG and for the same reason —
// Go's map order is randomised, and a load order that varied between
// processes would make one build produce two evidence records.
func Resolve(available []PackNode, roots ...PackID) ([]PackNode, error) {
	byID := make(map[PackID]PackNode, len(available))
	for _, n := range available {
		if prior, dup := byID[n.ID]; dup {
			return nil, failure(FailureDuplicate, n.ID, "offered at both %s and %s; a resolution holds one version of a pack", prior.Version, n.Version)
		}
		if !n.Level.Valid() {
			return nil, failure(FailureInvalidLevel, n.ID, "level %q is not one CONT-001 §10 defines", n.Level)
		}
		byID[n.ID] = n
	}

	if len(roots) == 0 {
		for id := range byID {
			roots = append(roots, id)
		}
	}
	closure, err := reach(byID, roots)
	if err != nil {
		return nil, err
	}

	ids := sortedIDs(closure)
	for _, id := range ids {
		n := closure[id]
		for _, d := range n.Dependencies {
			if d.Pack == n.ID {
				return nil, failure(FailureCycle, n.ID, "depends on itself")
			}
			if d.Constraint.IsZero() {
				return nil, failure(FailureNoConstraint, n.ID, "depends on %s with no version constraint", d.Pack)
			}
			dep := closure[d.Pack] // reach guarantees presence
			if !d.Constraint.Allows(dep.Version) {
				return nil, failure(FailureUnsatisfied, n.ID, "requires %s %s, and %s is available", d.Pack, d.Constraint, dep.Version)
			}
			if !n.Level.CanDependOn(dep.Level) {
				return nil, failure(FailureLevel, n.ID,
					"is %s and depends on %s, which is %s; a pack depends only on its own level or a more general one", n.Level, d.Pack, dep.Level)
			}
		}
		for _, c := range n.Conflicts {
			other, ok := closure[c.Pack]
			if ok && c.Constraint.Allows(other.Version) {
				return nil, failure(FailureConflict, n.ID, "declares a conflict with %s %s, and %s is in the resolution", c.Pack, c.Constraint, other.Version)
			}
		}
	}

	return order(closure, ids)
}

// reach collects the roots and everything they transitively depend on.
func reach(byID map[PackID]PackNode, roots []PackID) (map[PackID]PackNode, error) {
	closure := map[PackID]PackNode{}
	stack := append([]PackID(nil), roots...)
	sort.Slice(stack, func(i, j int) bool { return stack[i] < stack[j] })
	for _, r := range stack {
		if _, ok := byID[r]; !ok {
			return nil, failure(FailureUnknownRoot, r, "is not among the available packs")
		}
	}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, done := closure[id]; done {
			continue
		}
		n := byID[id]
		closure[id] = n
		for _, d := range n.Dependencies {
			if _, ok := byID[d.Pack]; !ok {
				return nil, failure(FailureMissing, n.ID, "depends on %s %s, which is not available", d.Pack, d.Constraint)
			}
			stack = append(stack, d.Pack)
		}
	}
	return closure, nil
}

func order(closure map[PackID]PackNode, ids []PackID) ([]PackNode, error) {
	indegree := make(map[PackID]int, len(closure))
	dependents := make(map[PackID][]PackID, len(closure))
	for _, id := range ids {
		indegree[id] += 0
		for _, d := range closure[id].Dependencies {
			indegree[id]++
			dependents[d.Pack] = append(dependents[d.Pack], id)
		}
	}

	less := func(a, b PackID) bool {
		la, lb := closure[a].Level.Rank(), closure[b].Level.Rank()
		if la != lb {
			return la < lb
		}
		return a < b
	}
	var ready []PackID
	for _, id := range ids {
		if indegree[id] == 0 {
			ready = append(ready, id)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return less(ready[i], ready[j]) })

	out := make([]PackNode, 0, len(closure))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		out = append(out, closure[id])
		for _, d := range dependents[id] {
			indegree[d]--
			if indegree[d] == 0 {
				ready = append(ready, d)
			}
		}
		sort.Slice(ready, func(i, j int) bool { return less(ready[i], ready[j]) })
	}

	if len(out) != len(closure) {
		return nil, failure(FailureCycle, cycleStart(indegree), "the dependency graph is cyclic: %s", describeCycle(closure, indegree))
	}
	return out, nil
}

// cycleStart and describeCycle name a cycle rather than reporting that one
// exists: a content engineer fixing it needs the packs, not the fact.
func cycleStart(indegree map[PackID]int) PackID {
	var stuck []PackID
	for id, d := range indegree {
		if d > 0 {
			stuck = append(stuck, id)
		}
	}
	sort.Slice(stuck, func(i, j int) bool { return stuck[i] < stuck[j] })
	return stuck[0]
}

func describeCycle(closure map[PackID]PackNode, indegree map[PackID]int) string {
	// Walk dependency edges from the smallest stuck pack, staying among stuck
	// packs, until a pack repeats. Every stuck pack has at least one stuck
	// dependency (otherwise it would have become ready), so the walk ends in a
	// cycle.
	at := cycleStart(indegree)
	seen := map[PackID]int{}
	var path []PackID
	for {
		if i, ok := seen[at]; ok {
			cyc := append(path[i:], at)
			parts := make([]string, len(cyc))
			for j, p := range cyc {
				parts[j] = string(p)
			}
			return strings.Join(parts, " → ")
		}
		seen[at] = len(path)
		path = append(path, at)
		deps := closure[at].Dependencies
		next := PackID("")
		for _, d := range deps {
			if indegree[d.Pack] > 0 && (next == "" || d.Pack < next) {
				next = d.Pack
			}
		}
		at = next
	}
}

func sortedIDs(m map[PackID]PackNode) []PackID {
	out := make([]PackID, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
