package rule

import (
	"fmt"
	"sort"
	"sync/atomic"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
)

// Bundle is a compiled, verified, immutable content bundle.
//
// It is built once by Load and never mutated. That is what makes ADR-0009 §2.5
// true — the one piece of in-memory state a cell holds is this, it is immutable,
// version-identified and swapped atomically, so any replica can serve any
// request for its cell.
type Bundle struct {
	id        string
	digest    string
	irVersion int

	nodes map[NodeID]Node
	// order is the topological order Load computed. Evaluation walks it rather
	// than recursing, so evaluation cost is bounded by the node count and there
	// is no stack to overflow on deep content.
	order []NodeID
	// roots are the nodes whose results a determination reads.
	roots []NodeID

	moneyConsts    map[string]fiscal.Money
	rateConsts     map[string]fiscal.Rate
	quantityConsts map[string]fiscal.Quantity
	stringConsts   map[string]string
}

// ID returns the bundle identity recorded in every decision.
func (b *Bundle) ID() string { return b.id }

// Digest returns the bundle's content digest (ADR-0011 §2.3).
func (b *Bundle) Digest() string { return b.digest }

// IRVersion returns the instruction-set version the bundle was compiled for.
func (b *Bundle) IRVersion() int { return b.irVersion }

// NodeCount reports the size of the graph, for telemetry.
func (b *Bundle) NodeCount() int { return len(b.nodes) }

// Manifest is the wire form of a bundle, as the content plane signs it.
type Manifest struct {
	BundleID  string   `json:"bundleId"`
	Digest    string   `json:"digest"`
	IRVersion int      `json:"irVersion"`
	Nodes     []Node   `json:"nodes"`
	Roots     []NodeID `json:"roots"`
	// Constants are canonical decimal strings with their currency or unit, so
	// a constant pool cannot smuggle a float into content.
	Constants []Constant `json:"constants"`
}

// Constant is one entry in the pool.
type Constant struct {
	Name  string `json:"name"`
	Type  Type   `json:"type"`
	Value string `json:"value"`
	// Currency for MONEY, Unit for QUANTITY, Basis for RATE.
	Currency string `json:"currency,omitempty"`
	Unit     string `json:"unit,omitempty"`
	Basis    string `json:"basis,omitempty"`
}

// Load verifies a manifest and builds an immutable bundle.
//
// ADR-0005 §2.6's sequence, in order, and every step is fatal to the whole
// bundle: verify IR compatibility, type-check every node, resolve every
// reference, check the graph is acyclic. There is no partial activation — a
// bundle that fails any step is rejected whole, and the caller keeps the
// bundle it already had.
//
// Signature verification is the one step not here: it is a KMS call
// (ADR-0017 §2.6) and belongs at the boundary that fetches the bundle, so that
// this package stays free of I/O and stays testable without a key.
func Load(m Manifest) (*Bundle, error) {
	if m.BundleID == "" || m.Digest == "" {
		return nil, fmt.Errorf("rule: bundle names no id or digest")
	}
	if m.IRVersion < MinSupportedIRVersion || m.IRVersion > IRVersion {
		// Refuse rather than degrade (ADR-0005 §2.8). A runtime that executed
		// a bundle it only partly understands would produce decisions nobody
		// can replay.
		return nil, fmt.Errorf("rule: bundle requires IR version %d, this runtime supports %d to %d",
			m.IRVersion, MinSupportedIRVersion, IRVersion)
	}
	if len(m.Nodes) == 0 {
		return nil, fmt.Errorf("rule: bundle %s has no nodes", m.BundleID)
	}

	b := &Bundle{
		id:             m.BundleID,
		digest:         m.Digest,
		irVersion:      m.IRVersion,
		nodes:          make(map[NodeID]Node, len(m.Nodes)),
		roots:          append([]NodeID(nil), m.Roots...),
		moneyConsts:    map[string]fiscal.Money{},
		rateConsts:     map[string]fiscal.Rate{},
		quantityConsts: map[string]fiscal.Quantity{},
		stringConsts:   map[string]string{},
	}

	for _, c := range m.Constants {
		if err := b.addConstant(c); err != nil {
			return nil, err
		}
	}

	for _, n := range m.Nodes {
		if err := n.validate(); err != nil {
			return nil, err
		}
		if _, dup := b.nodes[n.ID]; dup {
			return nil, fmt.Errorf("rule: bundle %s declares node %s twice", m.BundleID, n.ID)
		}
		b.nodes[n.ID] = n
	}

	// Every reference resolves, including constants. A dangling reference found
	// at evaluation time would be a defect discovered by a customer.
	for _, n := range b.nodes {
		for _, arg := range n.Args {
			if _, ok := b.nodes[arg]; !ok {
				return nil, fmt.Errorf("rule: node %s references unknown node %s", n.ID, arg)
			}
		}
		if n.Op == OpConst && !b.hasConstant(n.Const, n.Type) {
			return nil, fmt.Errorf("rule: node %s references unknown %s constant %q", n.ID, n.Type, n.Const)
		}
	}
	for _, r := range b.roots {
		if _, ok := b.nodes[r]; !ok {
			return nil, fmt.Errorf("rule: bundle %s names unknown root %s", m.BundleID, r)
		}
	}

	order, err := topoSort(b.nodes)
	if err != nil {
		return nil, err
	}
	b.order = order
	return b, nil
}

func (b *Bundle) addConstant(c Constant) error {
	if c.Name == "" {
		return fmt.Errorf("rule: constant has no name")
	}
	switch c.Type {
	case TypeMoney:
		m, err := fiscal.ParseMoney(c.Value, fiscal.Currency(c.Currency))
		if err != nil {
			return fmt.Errorf("rule: constant %q: %w", c.Name, err)
		}
		b.moneyConsts[c.Name] = m
	case TypeRate:
		r, err := fiscal.ParseRate(c.Value, fiscal.RateBasis(c.Basis))
		if err != nil {
			return fmt.Errorf("rule: constant %q: %w", c.Name, err)
		}
		b.rateConsts[c.Name] = r
	case TypeQuantity:
		q, err := fiscal.ParseQuantity(c.Value, fiscal.Unit(c.Unit))
		if err != nil {
			return fmt.Errorf("rule: constant %q: %w", c.Name, err)
		}
		b.quantityConsts[c.Name] = q
	case TypeString, TypeReason:
		b.stringConsts[c.Name] = c.Value
	default:
		return fmt.Errorf("rule: constant %q has unknown type %q", c.Name, c.Type)
	}
	return nil
}

func (b *Bundle) hasConstant(name string, t Type) bool {
	switch t {
	case TypeMoney:
		_, ok := b.moneyConsts[name]
		return ok
	case TypeRate:
		_, ok := b.rateConsts[name]
		return ok
	case TypeQuantity:
		_, ok := b.quantityConsts[name]
		return ok
	case TypeString, TypeReason:
		_, ok := b.stringConsts[name]
		return ok
	}
	return false
}

// topoSort orders the graph and proves it is acyclic.
//
// Kahn's algorithm, with the ready set kept sorted. The sort is not cosmetic:
// Go's map iteration order is randomised, and an evaluation order that varied
// between processes would make the execution trace vary between replicas for
// one input. ADR-0005 §2.4 requires map iteration never to be observable, and
// this is the place it would otherwise leak.
func topoSort(nodes map[NodeID]Node) ([]NodeID, error) {
	indegree := make(map[NodeID]int, len(nodes))
	dependents := make(map[NodeID][]NodeID, len(nodes))

	for id := range nodes {
		if _, seen := indegree[id]; !seen {
			indegree[id] = 0
		}
	}
	for id, n := range nodes {
		for _, arg := range n.Args {
			indegree[id]++
			dependents[arg] = append(dependents[arg], id)
		}
	}

	ready := make([]NodeID, 0, len(nodes))
	for id, d := range indegree {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })

	order := make([]NodeID, 0, len(nodes))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)

		next := dependents[id]
		sort.Slice(next, func(i, j int) bool { return next[i] < next[j] })
		for _, d := range next {
			indegree[d]--
			if indegree[d] == 0 {
				ready = append(ready, d)
				sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })
			}
		}
	}

	if len(order) != len(nodes) {
		// The remaining nodes are exactly those in a cycle. Naming one is more
		// useful to a content author than saying "there is a cycle".
		var stuck []NodeID
		for id, d := range indegree {
			if d > 0 {
				stuck = append(stuck, id)
			}
		}
		sort.Slice(stuck, func(i, j int) bool { return stuck[i] < stuck[j] })
		return nil, fmt.Errorf("rule: bundle is cyclic; %d nodes unreachable, including %s", len(stuck), stuck[0])
	}
	return order, nil
}

// Holder publishes a bundle by atomic pointer swap (ADR-0005 §2.6).
//
// Load, verification and graph construction all happen before the swap, so no
// request blocks on any of it and no request ever sees a half-built bundle. A
// replacement that fails verification never reaches Publish, so the runtime
// keeps serving the bundle it already had.
type Holder struct{ current atomic.Pointer[Bundle] }

// Publish makes b the active bundle.
func (h *Holder) Publish(b *Bundle) { h.current.Store(b) }

// Current returns the active bundle, or nil if none has been published. A nil
// result is CategoryUnavailable rather than an internal error: a cell that has
// not yet loaded content will, and retry is genuinely safe.
func (h *Holder) Current() *Bundle { return h.current.Load() }
