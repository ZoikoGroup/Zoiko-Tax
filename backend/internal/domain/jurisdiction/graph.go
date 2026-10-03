package jurisdiction

import (
	"fmt"
	"sort"
	"time"
)

// Node is a jurisdiction as it stands in one version of the graph
// (ZTAX-JUR-001 §2.1). It carries the effective period the base Jurisdiction
// does not: a node exists as a taxing jurisdiction from EffectiveFrom, and a
// node that ceases to exist is closed by EffectiveTo, never removed
// (JUR-REQ-0002), so historical decisions keep resolving.
type Node struct {
	Jurisdiction
	EffectiveFrom time.Time
	// EffectiveTo is exclusive; nil is open-ended.
	EffectiveTo *time.Time
}

// ValidAt reports whether the node exists at t.
func (n Node) ValidAt(t time.Time) bool {
	if t.Before(n.EffectiveFrom) {
		return false
	}
	return n.EffectiveTo == nil || t.Before(*n.EffectiveTo)
}

// Membership is the explicit member list of a SPECIAL jurisdiction: the
// general jurisdictions whose transactions it also claims (JUR-001 §2.2). A
// SPECIAL node is resolved by its own boundary or by this list, never by the
// hierarchy.
type Membership struct {
	Special ID
	Members []ID
}

// Graph is one version of the jurisdiction graph: acyclic and single-parent,
// with SPECIAL as the declared exception (JUR-001 §2.2).
//
// It is built once by NewGraph and never changes; a correction is a new
// version. The fields are unexported so that holding a *Graph is holding a
// validated one.
type Graph struct {
	version string
	nodes   map[ID]Node
	members map[ID][]ID
}

// NewGraph validates nodes into a graph.
//
// Acyclicity is not checked by walking: every non-SPECIAL node's parent must
// sit at a strictly outer level, so any path toward the root strictly
// decreases in rank and cannot return to where it began. A graph that cannot
// have a cycle is better than one checked for cycles.
func NewGraph(version string, nodes []Node, memberships []Membership) (*Graph, error) {
	if version == "" {
		return nil, fmt.Errorf("jurisdiction: graph has no version")
	}
	g := &Graph{version: version, nodes: make(map[ID]Node, len(nodes)), members: map[ID][]ID{}}
	for _, n := range nodes {
		if err := validateNode(n); err != nil {
			return nil, err
		}
		if _, dup := g.nodes[n.ID]; dup {
			return nil, fmt.Errorf("jurisdiction: %s is declared twice in graph %s", n.ID, version)
		}
		g.nodes[n.ID] = n
	}
	for _, n := range g.nodes {
		if err := g.validateParent(n); err != nil {
			return nil, err
		}
	}
	for _, m := range memberships {
		special, ok := g.nodes[m.Special]
		if !ok || special.Level != LevelSpecial {
			return nil, fmt.Errorf("jurisdiction: membership list for %s, which is not a SPECIAL jurisdiction in graph %s", m.Special, version)
		}
		for _, id := range m.Members {
			member, ok := g.nodes[id]
			if !ok {
				return nil, fmt.Errorf("jurisdiction: %s lists member %s, which graph %s does not declare", m.Special, id, version)
			}
			if member.Level == LevelSpecial {
				return nil, fmt.Errorf("jurisdiction: %s lists %s, another SPECIAL jurisdiction, as a member", m.Special, id)
			}
		}
		members := append([]ID(nil), m.Members...)
		sort.Slice(members, func(i, j int) bool { return members[i] < members[j] })
		g.members[m.Special] = members
	}
	return g, nil
}

func validateNode(n Node) error {
	if err := n.ID.Validate(); err != nil {
		return err
	}
	switch {
	case !n.Level.Valid():
		return fmt.Errorf("jurisdiction: %s has level %q", n.ID, n.Level)
	case n.Name == "":
		return fmt.Errorf("jurisdiction: %s has no name", n.ID)
	case !countryCode(n.Country):
		return fmt.Errorf("jurisdiction: %s has country %q; it must be ISO 3166-1 alpha-2", n.ID, n.Country)
	case n.Timezone == "":
		return fmt.Errorf("jurisdiction: %s has no timezone", n.ID)
	case n.EffectiveFrom.IsZero():
		return fmt.Errorf("jurisdiction: %s has no effective-from date", n.ID)
	case n.EffectiveTo != nil && !n.EffectiveTo.After(n.EffectiveFrom):
		return fmt.Errorf("jurisdiction: %s closes on or before it opens", n.ID)
	}
	return nil
}

func countryCode(s string) bool {
	if len(s) != 2 {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func (g *Graph) validateParent(n Node) error {
	switch n.Level {
	case LevelCountry:
		if n.Parent != "" {
			return fmt.Errorf("jurisdiction: country %s names a parent", n.ID)
		}
		return nil
	case LevelSpecial:
		// A SPECIAL node MAY name the jurisdiction it sits in, for display;
		// resolution never walks through it either way.
		if n.Parent == "" {
			return nil
		}
	default:
		if n.Parent == "" {
			return fmt.Errorf("jurisdiction: %s %s has no parent", n.Level, n.ID)
		}
	}
	parent, ok := g.nodes[n.Parent]
	if !ok {
		return fmt.Errorf("jurisdiction: %s names parent %s, which graph %s does not declare", n.ID, n.Parent, g.version)
	}
	if parent.Level == LevelSpecial {
		return fmt.Errorf("jurisdiction: %s names SPECIAL %s as its parent", n.ID, n.Parent)
	}
	if parent.Country != n.Country {
		return fmt.Errorf("jurisdiction: %s is in %s and its parent %s is in %s", n.ID, n.Country, n.Parent, parent.Country)
	}
	pr, _ := parent.Level.Rank()
	nr, _ := n.Level.Rank()
	if pr >= nr {
		return fmt.Errorf("jurisdiction: %s (%s) names %s (%s) as parent; a parent sits at a strictly outer level",
			n.ID, n.Level, n.Parent, parent.Level)
	}
	return nil
}

// Version returns the graph's version, recorded on every resolution.
func (g *Graph) Version() string { return g.version }

// Node returns the node with the given id, whatever its period.
func (g *Graph) Node(id ID) (Node, bool) {
	n, ok := g.nodes[id]
	return n, ok
}

// At returns the node with the given id if it exists at t.
func (g *Graph) At(id ID, t time.Time) (Node, bool) {
	n, ok := g.nodes[id]
	if !ok || !n.ValidAt(t) {
		return Node{}, false
	}
	return n, true
}

// CountryAt returns the COUNTRY node for an ISO alpha-2 code at t.
func (g *Graph) CountryAt(code string, t time.Time) (Node, bool) {
	var found []Node
	for _, n := range g.nodes {
		if n.Level == LevelCountry && n.Country == code && n.ValidAt(t) {
			found = append(found, n)
		}
	}
	if len(found) != 1 {
		return Node{}, false
	}
	return found[0], true
}

// Chain returns the node and every ancestor, each valid at t, innermost
// first (JUR-001 §8.3 step 5). An ancestor that does not exist at t while its
// child does is a defect in the graph's periods, not an input problem, and is
// reported as one.
func (g *Graph) Chain(id ID, t time.Time) ([]Node, error) {
	n, ok := g.At(id, t)
	if !ok {
		return nil, fmt.Errorf("jurisdiction: %s does not exist at %s in graph %s", id, t.Format(time.RFC3339), g.version)
	}
	chain := []Node{n}
	if n.Level == LevelSpecial {
		return chain, nil
	}
	for n.Parent != "" {
		parent, ok := g.At(n.Parent, t)
		if !ok {
			return nil, fmt.Errorf("jurisdiction: %s exists at %s and its parent %s does not, in graph %s",
				n.ID, t.Format(time.RFC3339), n.Parent, g.version)
		}
		chain = append(chain, parent)
		n = parent
	}
	return chain, nil
}

// AncestorAt walks from id toward the root and returns the node at level, if
// the chain has one (JUR-001 §4.3.1's agreement level).
func (g *Graph) AncestorAt(id ID, level Level, t time.Time) (Node, bool) {
	chain, err := g.Chain(id, t)
	if err != nil {
		return Node{}, false
	}
	for _, n := range chain {
		if n.Level == level {
			return n, true
		}
	}
	return Node{}, false
}

// SpecialsFor returns every SPECIAL jurisdiction valid at t whose membership
// list names one of ids, sorted by id.
func (g *Graph) SpecialsFor(ids []ID, t time.Time) []Node {
	want := map[ID]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []Node
	for special, members := range g.members {
		n, ok := g.At(special, t)
		if !ok {
			continue
		}
		for _, m := range members {
			if want[m] {
				out = append(out, n)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
